package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/spf13/cobra"
)

func (r *Runner) workersCommand() *cobra.Command {
	workers := &cobra.Command{Use: "workers", Short: "Access deployed workers"}
	showHelpWhenEmpty(workers)
	var apiURL, method, data string
	var headers []string
	var include, fail bool
	var timeout time.Duration
	request := &cobra.Command{
		Use:     "request <worker> <path>",
		Short:   "Make an authenticated HTTP request to a deployed worker",
		Long:    "Resolve a worker by ID or name in the active organization and request its public HTTPS endpoint with temporary machine authentication. Requires workers:read and workers:invoke. Redirects are returned without following them.",
		Example: "  nanoflare workers request my-worker /api/check\n  nanoflare workers request my-worker /api/items -X POST -H 'Content-Type: application/json' -d '{\"name\":\"test\"}'",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			method = strings.ToUpper(method)
			input := nanoflare.WorkerAccessRequest{Method: method, Path: args[1]}
			if err := nanoflare.ValidateWorkerAccessRequest(input); err != nil {
				return err
			}
			if timeout <= 0 {
				return errors.New("timeout must be positive")
			}
			// Validate caller headers before obtaining a credential or making requests.
			parsedHeaders := http.Header{}
			for _, header := range headers {
				name, value, ok := strings.Cut(header, ":")
				name = strings.TrimSpace(name)
				value = strings.TrimSpace(value)
				if !ok || name == "" || strings.ContainsAny(name, " \t\r\n") || strings.ContainsAny(value, "\r\n") {
					return fmt.Errorf("invalid header %q", header)
				}
				if strings.HasPrefix(strings.ToLower(name), "x-nanoflare-") || strings.HasPrefix(strings.ToLower(name), "x-forwarded-") || strings.EqualFold(name, "Host") || strings.EqualFold(name, "Forwarded") {
					return fmt.Errorf("header %q is reserved", name)
				}
				parsedHeaders.Add(name, value)
			}
			baseURL := projectAPIURL(apiURL)
			if apiURL == "" && os.Getenv("NANOFLARED_URL") == "" {
				if auth, _, err := resolveAuthConfig(); err == nil && auth.APIURL != "" {
					baseURL = strings.TrimRight(auth.APIURL, "/")
				}
			}
			var apps []nanoflare.App
			if err := r.request(http.MethodGet, baseURL+"/v1/workers", nil, &apps); err != nil {
				return err
			}
			var matches []nanoflare.App
			for _, app := range apps {
				if app.ID == args[0] {
					matches = []nanoflare.App{app}
					break
				}
				if app.Name == args[0] {
					matches = append(matches, app)
				}
			}
			if len(matches) != 1 {
				return fmt.Errorf("worker %q must identify exactly one worker in the active organization (found %d)", args[0], len(matches))
			}
			app := matches[0]
			if app.IsPrivate() || app.Hostname == "" {
				return errors.New("worker request requires a public worker hostname")
			}
			var access nanoflare.WorkerAccessToken
			if err := r.request(http.MethodPost, baseURL+"/v1/workers/"+url.PathEscape(app.ID)+"/access-token", input, &access); err != nil {
				return err
			}
			// Construct the destination from the resolved worker, never a token response URL.
			target, err := url.Parse("https://" + app.Hostname)
			if err != nil || target.Host != app.Hostname || target.User != nil || target.Path != "" || target.RawQuery != "" || target.Fragment != "" {
				return errors.New("server returned an invalid worker hostname")
			}
			path, _ := url.ParseRequestURI(input.Path)
			target.Path, target.RawPath, target.RawQuery, target.ForceQuery = path.Path, path.RawPath, path.RawQuery, path.ForceQuery
			if access.Token == "" || access.ExpiresAt.Before(time.Now()) {
				return errors.New("server returned a missing or expired worker access token")
			}
			var body io.Reader
			if cmd.Flags().Changed("data") {
				if data == "-" {
					body = r.Stdin
				} else {
					body = strings.NewReader(data)
				}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
			if err != nil {
				return err
			}
			req.Header = parsedHeaders
			req.Header.Set(nanoflare.WorkerAccessHeader, access.Token)
			client := r.Client
			if original, ok := client.(*http.Client); ok {
				clone := *original
				clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
				clone.Jar = nil // Do not attach unrelated browser or control-plane cookies.
				client = &clone
			}
			response, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("request worker: %w", err)
			}
			defer response.Body.Close()
			if include {
				fmt.Fprintf(r.Stdout, "%s %s\r\n", response.Proto, response.Status)
				if err := response.Header.Write(r.Stdout); err != nil {
					return err
				}
				fmt.Fprint(r.Stdout, "\r\n")
			}
			if _, err := io.Copy(r.Stdout, response.Body); err != nil {
				return err
			}
			if fail && response.StatusCode >= 400 {
				return fmt.Errorf("worker returned HTTP %d", response.StatusCode)
			}
			return nil
		},
	}
	request.Flags().StringVar(&apiURL, "api-url", "", "Nanoflare API URL")
	request.Flags().StringVarP(&method, "method", "X", http.MethodGet, "HTTP method")
	request.Flags().StringArrayVarP(&headers, "header", "H", nil, "Request header (repeatable)")
	request.Flags().StringVarP(&data, "data", "d", "", "Request body; use - to read stdin")
	request.Flags().BoolVarP(&include, "include", "i", false, "Include response status and headers")
	request.Flags().BoolVar(&fail, "fail", true, "Return an error for HTTP 4xx/5xx responses")
	request.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Worker request timeout")
	workers.AddCommand(request)
	return workers
}
