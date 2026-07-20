package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	starterworker "github.com/clas/nanoflare/templates/starter-worker"
)

const (
	projectFilename  = "nanoflare.json"
	defaultAPIURL    = "http://127.0.0.1:8080"
	authFilename     = "auth.json"
	authStorePathEnv = "NANOFLARE_AUTH_STORE"
)

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type Runner struct {
	Client HTTPClient
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	Now    func() time.Time
}

type Project struct {
	Name                 string                                 `json:"name"`
	Hostname             string                                 `json:"hostname,omitempty"`
	AppID                string                                 `json:"app_id,omitempty"`
	APIURL               string                                 `json:"api_url"`
	Entrypoint           string                                 `json:"entrypoint"`
	Format               string                                 `json:"format,omitempty"`
	CompatibilityDate    string                                 `json:"compatibility_date"`
	Triggers             nanoflare.TriggerConfig                `json:"triggers,omitempty"`
	Vars                 map[string]json.RawMessage             `json:"vars,omitempty"`
	Files                []string                               `json:"files"`
	KVNamespaces         []nanoflare.KVBinding                  `json:"kv_namespaces,omitempty"`
	Databases            []nanoflare.DatabaseBinding            `json:"db,omitempty"`
	ObjectStorageBuckets []nanoflare.ObjectStorageBucketBinding `json:"object_storage_buckets,omitempty"`
	Assets               ProjectAssets                          `json:"assets,omitempty"`
	Auth                 ProjectAuth                            `json:"auth,omitempty"`
}

type projectAlias struct {
	ObjectStorageBuckets      []legacyObjectStorageBucketBinding `json:"object_storage_buckets,omitempty"`
	ObjectStorageBucketLegacy []legacyObjectStorageBucketBinding `json:"object_storage_bucket,omitempty"`
}

type legacyObjectStorageBucketBinding struct {
	Binding  string `json:"binding"`
	ID       string `json:"id,omitempty"`
	BucketID string `json:"bucket_id,omitempty"`
}

type ProjectAssets struct {
	Binding          string                   `json:"binding,omitempty"`
	Directory        string                   `json:"directory,omitempty"`
	HTMLHandling     string                   `json:"html_handling,omitempty"`
	NotFoundHandling string                   `json:"not_found_handling,omitempty"`
	RunWorkerFirst   nanoflare.RunWorkerFirst `json:"run_worker_first,omitempty"`
}

type ProjectAuth struct {
	ProtectedRoutes []string `json:"protected_routes,omitempty"`
}

type AuthConfig struct {
	APIURL       string                   `json:"api_url"`
	Token        string                   `json:"token"`
	RefreshToken string                   `json:"refresh_token,omitempty"`
	ActiveOrgID  string                   `json:"active_org_id"`
	User         nanoflare.User           `json:"user"`
	Orgs         []nanoflare.Organization `json:"organizations"`
}

func NewRunner(stdout, stderr io.Writer) *Runner {
	return &Runner{
		Client: http.DefaultClient,
		Stdout: stdout,
		Stderr: stderr,
		Stdin:  os.Stdin,
		Now:    time.Now,
	}
}

func (r *Runner) Run(args []string) error {
	if len(args) == 0 {
		r.usage()
		return errors.New("command is required")
	}
	switch args[0] {
	case "init":
		return r.init(args[1:])
	case "create":
		return r.create(withoutWorkerNoun(args[1:]))
	case "list":
		return r.list(withoutWorkerNoun(args[1:]))
	case "delete":
		return r.delete(withoutWorkerNoun(args[1:]))
	case "deploy":
		return r.deploy(withoutWorkerNoun(args[1:]))
	case "kv":
		return r.kv(args[1:])
	case "db":
		return r.db(args[1:])
	case "object-storage":
		return r.objectStorage(args[1:])
	case "auth":
		return r.auth(args[1:])
	case "secret":
		return r.secret(args[1:])
	case "help", "-h", "--help":
		r.usage()
		return nil
	default:
		r.usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func (r *Runner) init(args []string) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	name := flags.String("name", "", "worker name")
	hostname := flags.String("hostname", "", "worker DNS hostname")
	apiURL := flags.String("api-url", envOrDefault("NANOFLARED_URL", defaultAPIURL), "nanoflared base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("usage: nanoflare init [flags] [directory]")
	}
	dir := "."
	if flags.NArg() == 1 {
		dir = flags.Arg(0)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return fmt.Errorf("create project directory: %w", err)
	}
	projectPath := filepath.Join(absDir, projectFilename)
	workerPath := filepath.Join(absDir, "worker.js")
	for _, path := range []string{projectPath, workerPath} {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	projectName := strings.TrimSpace(*name)
	if projectName == "" {
		projectName = filepath.Base(absDir)
	}
	projectHostname := strings.TrimSpace(*hostname)
	project := Project{
		Name:              projectName,
		Hostname:          projectHostname,
		APIURL:            strings.TrimRight(*apiURL, "/"),
		Entrypoint:        "worker.js",
		Format:            "modules",
		CompatibilityDate: r.Now().UTC().Format("2006-01-02"),
		Files:             []string{"worker.js"},
	}
	if err := writeProject(projectPath, project, os.O_EXCL); err != nil {
		return err
	}
	if err := os.WriteFile(workerPath, starterworker.WorkerJS, 0o644); err != nil {
		_ = os.Remove(projectPath)
		return fmt.Errorf("write starter worker: %w", err)
	}
	fmt.Fprintf(r.Stdout, "Initialized worker project in %s\n", absDir)
	fmt.Fprintln(r.Stdout, "Run `nanoflare create` to register it, then `nanoflare deploy`.")
	return nil
}

func (r *Runner) create(args []string) error {
	flags := flag.NewFlagSet("create", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", "", "nanoflared base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: nanoflare create [worker] [flags]")
	}
	path, project, err := loadProject()
	if err != nil {
		return err
	}
	if project.AppID != "" {
		return fmt.Errorf("worker is already registered as %s", project.AppID)
	}
	baseURL := projectAPIURL(project, *apiURL)
	var app nanoflare.App
	if err := r.request(http.MethodPost, baseURL+"/v1/apps", nanoflare.CreateAppInput{
		Name:     project.Name,
		Hostname: project.Hostname,
		Auth:     nanoflare.AuthConfig{ProtectedRoutes: append([]string(nil), project.Auth.ProtectedRoutes...)},
	}, &app); err != nil {
		return err
	}
	project.AppID = app.ID
	project.APIURL = baseURL
	if err := writeProject(path, project, os.O_TRUNC); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Created worker %s (%s)\n", app.ID, app.Hostname)
	return nil
}

func (r *Runner) list(args []string) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", envOrDefault("NANOFLARED_URL", defaultAPIURL), "nanoflared base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: nanoflare list [worker] [flags]")
	}
	var apps []nanoflare.App
	if err := r.request(http.MethodGet, strings.TrimRight(*apiURL, "/")+"/v1/apps", nil, &apps); err != nil {
		return err
	}
	for _, app := range apps {
		fmt.Fprintf(r.Stdout, "%s\t%s\t%s\n", app.ID, app.Name, app.Hostname)
	}
	return nil
}

func (r *Runner) delete(args []string) error {
	flags := flag.NewFlagSet("delete", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", "", "nanoflared base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("usage: nanoflare delete [worker] [app-id] [flags]")
	}
	appID := ""
	var projectPath string
	var project Project
	if flags.NArg() == 1 {
		appID = strings.TrimSpace(flags.Arg(0))
	} else {
		var err error
		projectPath, project, err = loadProject()
		if err != nil {
			return err
		}
		if project.AppID == "" {
			return errors.New("worker is not registered; run `nanoflare create` first")
		}
		appID = project.AppID
	}
	baseURL := projectAPIURL(project, *apiURL)
	if err := r.request(http.MethodDelete, baseURL+"/v1/apps/"+appID, nil, nil); err != nil {
		return err
	}
	if projectPath != "" && project.AppID == appID {
		project.AppID = ""
		project.APIURL = baseURL
		if err := writeProject(projectPath, project, os.O_TRUNC); err != nil {
			return err
		}
	}
	fmt.Fprintf(r.Stdout, "Deleted worker %s\n", appID)
	return nil
}

func (r *Runner) deploy(args []string) error {
	flags := flag.NewFlagSet("deploy", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", "", "nanoflared base URL")
	compatibilityDate := flags.String("compatibility-date", "", "worker compatibility date (YYYY-MM-DD)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: nanoflare deploy [worker] [flags]")
	}
	projectPath, project, err := loadProject()
	if err != nil {
		return err
	}
	if project.AppID == "" {
		return errors.New("worker is not registered; run `nanoflare create` first")
	}
	date := project.CompatibilityDate
	if *compatibilityDate != "" {
		date = *compatibilityDate
	}
	baseURL := projectAPIURL(project, *apiURL)
	if err := r.request(http.MethodPatch, baseURL+"/v1/apps/"+project.AppID, nanoflare.UpdateAppInput{
		Auth: &nanoflare.AuthConfig{
			ProtectedRoutes: append([]string(nil), project.Auth.ProtectedRoutes...),
		},
	}, nil); err != nil {
		return err
	}
	files, err := loadWorkerFiles(project.Files)
	if err != nil {
		return err
	}
	assets, err := loadAssetFiles(project.Assets.Directory)
	if err != nil {
		return err
	}
	commitHash, commitMessage := deploymentGitMetadata(filepath.Dir(projectPath))
	var deployment nanoflare.Deployment
	if err := r.request(http.MethodPost, baseURL+"/v1/apps/"+project.AppID+"/deployments", nanoflare.DeployInput{
		CommitHash:           commitHash,
		CommitMessage:        commitMessage,
		Files:                files,
		Assets:               assets,
		Entrypoint:           project.Entrypoint,
		Format:               project.Format,
		CompatibilityDate:    date,
		Triggers:             project.Triggers,
		Vars:                 cloneProjectVars(project.Vars),
		KVNamespaces:         append([]nanoflare.KVBinding(nil), project.KVNamespaces...),
		Databases:            append([]nanoflare.DatabaseBinding(nil), project.Databases...),
		ObjectStorageBuckets: append([]nanoflare.ObjectStorageBucketBinding(nil), project.ObjectStorageBuckets...),
		AssetConfig: nanoflare.AssetConfig{
			Binding:          project.Assets.Binding,
			HTMLHandling:     project.Assets.HTMLHandling,
			NotFoundHandling: project.Assets.NotFoundHandling,
			RunWorkerFirst:   project.Assets.RunWorkerFirst,
		},
	}, &deployment); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Deployed worker %s as deployment %s\n", project.AppID, deployment.ID)
	return nil
}

func deploymentGitMetadata(dir string) (string, string) {
	commitHash, ok := gitOutput(dir, "rev-parse", "HEAD")
	if !ok {
		return "", ""
	}
	commitMessage, _ := gitOutput(dir, "log", "-1", "--pretty=%B")
	return commitHash, commitMessage
}

func gitOutput(dir string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	output, err := command.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(output)), true
}

func (r *Runner) auth(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: nanoflare auth <login|orgs|use-org|whoami|logout>")
	}
	switch args[0] {
	case "login":
		return r.authLogin(args[1:])
	case "orgs":
		return r.authOrgs(args[1:])
	case "use-org":
		return r.authUseOrg(args[1:])
	case "whoami":
		return r.authWhoami(args[1:])
	case "logout":
		return r.authLogout(args[1:])
	default:
		return fmt.Errorf("unknown auth command %q", args[0])
	}
}

func (r *Runner) authLogin(args []string) error {
	flags := flag.NewFlagSet("auth login", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", envOrDefault("NANOFLARED_URL", defaultAPIURL), "nanoflared base URL")
	email := flags.String("email", "", "user email")
	password := flags.String("password", "", "user password")
	webLogin := flags.Bool("web", false, "use browser login flow")
	patLogin := flags.Bool("pat", false, "use personal access token login flow")
	setupOrg := flags.String("setup-org", "", "create first user and organization when setup has not run")
	if err := flags.Parse(args); err != nil {
		return err
	}
	baseURL := strings.TrimRight(*apiURL, "/")
	if *patLogin {
		if strings.TrimSpace(*setupOrg) != "" {
			return errors.New("--setup-org cannot be used with --pat")
		}
		if *webLogin {
			return errors.New("--web cannot be used with --pat")
		}
		return r.authLoginPAT(baseURL)
	}
	if *webLogin {
		if strings.TrimSpace(*setupOrg) != "" {
			return errors.New("--setup-org cannot be used with --web")
		}
		return r.authLoginWeb(baseURL)
	}
	reader := bufio.NewReader(r.Stdin)
	if strings.TrimSpace(*email) == "" {
		fmt.Fprint(r.Stderr, "Email: ")
		value, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		*email = strings.TrimSpace(value)
	}
	if strings.TrimSpace(*password) == "" {
		fmt.Fprint(r.Stderr, "Password: ")
		value, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		*password = strings.TrimSpace(value)
	}
	path := baseURL + "/v1/auth/login"
	var input any = nanoflare.LoginInput{Email: *email, Password: *password}
	if strings.TrimSpace(*setupOrg) != "" {
		path = baseURL + "/v1/setup/signup"
		input = nanoflare.SignupInput{Email: *email, Password: *password, OrganizationName: *setupOrg}
	}
	var session nanoflare.AuthSession
	if err := r.requestNoAuth(http.MethodPost, path, input, &session); err != nil {
		return err
	}
	auth, err := authConfigFromSession(baseURL, session, "")
	if err != nil {
		return err
	}
	if err := writeAuthConfig(auth); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Logged in as %s\n", auth.User.Email)
	if auth.ActiveOrgID != "" {
		fmt.Fprintf(r.Stdout, "Using organization %s\n", auth.ActiveOrgID)
	}
	return nil
}

func (r *Runner) authLoginPAT(baseURL string) error {
	fmt.Fprint(r.Stderr, "Personal access token: ")
	value, err := bufio.NewReader(r.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	token := strings.TrimSpace(value)
	if token == "" {
		return errors.New("personal access token is required")
	}
	var session nanoflare.AuthSession
	if err := r.requestNoAuth(http.MethodPost, baseURL+"/v1/auth/pat/session", map[string]string{"token": token}, &session); err != nil {
		return err
	}
	session.Token = token
	session.RefreshToken = ""
	auth, err := authConfigFromSession(baseURL, session, "")
	if err != nil {
		return err
	}
	auth.RefreshToken = ""
	if err := writeAuthConfig(auth); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Logged in as %s\n", auth.User.Email)
	if auth.ActiveOrgID != "" {
		fmt.Fprintf(r.Stdout, "Using organization %s\n", auth.ActiveOrgID)
	}
	return nil
}

func (r *Runner) authLoginWeb(baseURL string) error {
	callback, err := startWebLoginCallback()
	if err != nil {
		return fmt.Errorf("start local login callback: %w", err)
	}
	defer callback.Close()

	next := url.Values{}
	next.Set("callback_url", callback.URL)
	next.Set("state", callback.State)
	values := url.Values{}
	values.Set("next", "/cli-login?"+next.Encode())
	loginURL := baseURL + "/login?" + values.Encode()
	fmt.Fprintf(r.Stderr, "Open this URL to continue web login:\n%s\n", loginURL)
	if err := openBrowserFunc(loginURL); err == nil {
		fmt.Fprintln(r.Stderr, "Opened browser for web login.")
	} else {
		fmt.Fprintf(r.Stderr, "Could not open browser automatically: %v\n", err)
	}
	fmt.Fprintln(r.Stderr, "Waiting for browser login to complete...")
	code, err := callback.Wait(5 * time.Minute)
	if err != nil {
		return err
	}
	var session nanoflare.AuthSession
	if err := r.requestNoAuth(http.MethodPost, baseURL+"/v1/auth/cli/session", map[string]string{"code": code}, &session); err != nil {
		return err
	}
	auth, err := authConfigFromSession(baseURL, session, "")
	if err != nil {
		return err
	}
	if err := writeAuthConfig(auth); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Logged in as %s\n", auth.User.Email)
	if auth.ActiveOrgID != "" {
		fmt.Fprintf(r.Stdout, "Using organization %s\n", auth.ActiveOrgID)
	}
	return nil
}

type webLoginCallback struct {
	URL    string
	State  string
	server *http.Server
	done   chan webLoginResult
}

type webLoginResult struct {
	code string
	err  error
}

func startWebLoginCallback() (*webLoginCallback, error) {
	state, err := randomWebLoginState()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	callback := &webLoginCallback{
		URL:   "http://" + listener.Addr().String() + "/cli-login-callback",
		State: state,
		done:  make(chan webLoginResult, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/cli-login-callback", callback.handle)
	callback.server = &http.Server{Handler: mux}
	go func() {
		if err := callback.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			callback.deliver(webLoginResult{err: err})
		}
	}()
	return callback, nil
}

func (c *webLoginCallback) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/cli-login-callback" {
		http.NotFound(w, r)
		return
	}
	if subtleCompare(r.URL.Query().Get("state"), c.State) != nil {
		http.Error(w, "login state did not match", http.StatusBadRequest)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		http.Error(w, "code is required", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8"><title>Nanoflare CLI Login</title><style>body{font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:3rem;line-height:1.5;color:#111827}</style></head><body><h1>Nanoflare CLI login complete</h1><p>You can close this tab and return to your terminal.</p></body></html>`)
	c.deliver(webLoginResult{code: code})
}

func (c *webLoginCallback) deliver(result webLoginResult) {
	select {
	case c.done <- result:
	default:
	}
}

func (c *webLoginCallback) Wait(timeout time.Duration) (string, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-c.done:
		if result.err != nil {
			return "", result.err
		}
		return result.code, nil
	case <-timer.C:
		return "", errors.New("timed out waiting for browser login")
	}
}

func (c *webLoginCallback) Close() {
	if c.server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.server.Shutdown(ctx)
}

func randomWebLoginState() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func subtleCompare(left, right string) error {
	if len(left) != len(right) {
		return errors.New("values differ")
	}
	if subtle.ConstantTimeCompare([]byte(left), []byte(right)) != 1 {
		return errors.New("values differ")
	}
	return nil
}

func (r *Runner) authOrgs(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: nanoflare auth orgs")
	}
	auth, err := loadAuthConfig()
	if err != nil {
		return err
	}
	for _, org := range auth.Orgs {
		prefix := " "
		if org.ID == auth.ActiveOrgID {
			prefix = "*"
		}
		fmt.Fprintf(r.Stdout, "%s %s\t%s\n", prefix, org.ID, org.Name)
	}
	return nil
}

func (r *Runner) authUseOrg(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: nanoflare auth use-org <org-id>")
	}
	auth, err := loadAuthConfig()
	if err != nil {
		return err
	}
	for _, org := range auth.Orgs {
		if org.ID == args[0] {
			auth.ActiveOrgID = org.ID
			if err := writeAuthConfig(auth); err != nil {
				return err
			}
			fmt.Fprintf(r.Stdout, "Using organization %s\n", org.ID)
			return nil
		}
	}
	return fmt.Errorf("organization %s is not available to this user", args[0])
}

func (r *Runner) authWhoami(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: nanoflare auth whoami")
	}
	auth, err := loadAuthConfig()
	if err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "%s\n", auth.User.Email)
	if auth.ActiveOrgID != "" {
		fmt.Fprintf(r.Stdout, "org\t%s\n", auth.ActiveOrgID)
	}
	return nil
}

func (r *Runner) authLogout(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: nanoflare auth logout")
	}
	path, err := authConfigPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintln(r.Stdout, "Logged out")
	return nil
}

func (r *Runner) requestNoAuth(method, url string, input, output any) error {
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := r.Client.Do(request)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&apiError); err != nil || apiError.Error == "" {
			return fmt.Errorf("%s %s: nanoflared returned %s", method, url, response.Status)
		}
		return fmt.Errorf("%s %s: %s", method, url, apiError.Error)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("decode nanoflared response: %w", err)
	}
	return nil
}

func (r *Runner) request(method, url string, input, output any) error {
	var payload []byte
	if input != nil {
		var err error
		payload, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	auth, authErr := loadAuthConfig()
	response, err := r.authenticatedRequest(method, url, payload, input != nil, auth)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, url, err)
	}
	if response.StatusCode == http.StatusUnauthorized && authErr == nil && auth.RefreshToken != "" {
		response.Body.Close()
		refreshed, err := r.refreshAuthConfig(auth)
		if err != nil {
			return fmt.Errorf("refresh auth token: %w", err)
		}
		response, err = r.authenticatedRequest(method, url, payload, input != nil, refreshed)
		if err != nil {
			return fmt.Errorf("%s %s: %w", method, url, err)
		}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&apiError); err != nil || apiError.Error == "" {
			return fmt.Errorf("%s %s: nanoflared returned %s", method, url, response.Status)
		}
		return fmt.Errorf("%s %s: %s", method, url, apiError.Error)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("decode nanoflared response: %w", err)
	}
	return nil
}

func (r *Runner) authenticatedRequest(method, target string, payload []byte, hasInput bool, auth AuthConfig) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequest(method, target, body)
	if err != nil {
		return nil, err
	}
	if hasInput {
		request.Header.Set("Content-Type", "application/json")
	}
	if auth.Token != "" {
		request.Header.Set("Authorization", "Bearer "+auth.Token)
		if auth.ActiveOrgID != "" {
			request.Header.Set("X-Nanoflare-Org-ID", auth.ActiveOrgID)
		}
	}
	return r.Client.Do(request)
}

func (r *Runner) refreshAuthConfig(auth AuthConfig) (AuthConfig, error) {
	var session nanoflare.AuthSession
	if err := r.requestNoAuth(http.MethodPost, strings.TrimRight(auth.APIURL, "/")+"/v1/auth/refresh", map[string]string{"refresh_token": auth.RefreshToken}, &session); err != nil {
		return AuthConfig{}, err
	}
	refreshed, err := authConfigFromSession(auth.APIURL, session, auth.ActiveOrgID)
	if err != nil {
		return AuthConfig{}, err
	}
	if err := writeAuthConfig(refreshed); err != nil {
		return AuthConfig{}, err
	}
	return refreshed, nil
}

func loadProject() (string, Project, error) {
	path, err := filepath.Abs(projectFilename)
	if err != nil {
		return "", Project{}, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", Project{}, fmt.Errorf("read %s: %w", path, err)
	}
	var project Project
	if err := json.Unmarshal(content, &project); err != nil {
		return "", Project{}, fmt.Errorf("decode %s: %w", path, err)
	}
	var alias projectAlias
	if err := json.Unmarshal(content, &alias); err != nil {
		return "", Project{}, fmt.Errorf("decode %s aliases: %w", path, err)
	}
	if len(project.ObjectStorageBuckets) == 0 || hasEmptyObjectStorageBucketIDs(project.ObjectStorageBuckets) {
		legacy := alias.ObjectStorageBuckets
		if len(legacy) == 0 {
			legacy = alias.ObjectStorageBucketLegacy
		}
		if len(legacy) > 0 {
			project.ObjectStorageBuckets = project.ObjectStorageBuckets[:0]
		}
		for _, binding := range legacy {
			bucketID := strings.TrimSpace(binding.BucketID)
			if bucketID == "" {
				bucketID = strings.TrimSpace(binding.ID)
			}
			project.ObjectStorageBuckets = append(project.ObjectStorageBuckets, nanoflare.ObjectStorageBucketBinding{
				Binding:  binding.Binding,
				BucketID: bucketID,
			})
		}
	}
	if project.Name == "" || project.Entrypoint == "" || project.CompatibilityDate == "" || len(project.Files) == 0 {
		return "", Project{}, fmt.Errorf("%s is missing required worker configuration", path)
	}
	return path, project, nil
}

func hasEmptyObjectStorageBucketIDs(bindings []nanoflare.ObjectStorageBucketBinding) bool {
	for _, binding := range bindings {
		if strings.TrimSpace(binding.BucketID) == "" {
			return true
		}
	}
	return false
}

func cloneProjectVars(vars map[string]json.RawMessage) map[string]json.RawMessage {
	if len(vars) == 0 {
		return nil
	}
	cloned := make(map[string]json.RawMessage, len(vars))
	for name, value := range vars {
		cloned[name] = append(json.RawMessage(nil), value...)
	}
	return cloned
}

func loadWorkerFiles(paths []string) ([]nanoflare.WorkerFile, error) {
	files := make([]nanoflare.WorkerFile, 0, len(paths))
	for _, path := range paths {
		clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(path)))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("worker file path %q must remain inside the project", path)
		}
		content, err := os.ReadFile(clean)
		if err != nil {
			return nil, fmt.Errorf("read worker file %s: %w", clean, err)
		}
		files = append(files, nanoflare.WorkerFile{Path: filepath.ToSlash(clean), Content: string(content)})
	}
	return files, nil
}

func loadAssetFiles(dir string) ([]nanoflare.AssetFile, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}
	cleanRoot := filepath.Clean(filepath.FromSlash(dir))
	if cleanRoot == "." || filepath.IsAbs(cleanRoot) || cleanRoot == ".." || strings.HasPrefix(cleanRoot, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("asset directory %q must remain inside the project", dir)
	}
	var assets []nanoflare.AssetFile
	err := filepath.WalkDir(cleanRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(cleanRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(filepath.Clean(relative))
		if relative == "." || strings.HasPrefix(relative, "../") {
			return fmt.Errorf("asset file path %q must remain inside %s", path, cleanRoot)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read asset file %s: %w", path, err)
		}
		assets = append(assets, nanoflare.AssetFile{
			Path:        relative,
			Size:        int64(len(data)),
			ContentType: detectContentType(relative),
			Data:        data,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return assets, nil
}

func detectContentType(path string) string {
	if value := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); value != "" {
		return value
	}
	return "application/octet-stream"
}

func writeProject(path string, project Project, flag int) error {
	content, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|flag, 0o644)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func authConfigPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv(authStorePathEnv)); path != "" {
		return path, nil
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "nanoflare", authFilename), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "nanoflare", authFilename), nil
}

func loadAuthConfig() (AuthConfig, error) {
	path, err := authConfigPath()
	if err != nil {
		return AuthConfig{}, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return AuthConfig{}, fmt.Errorf("read auth config: %w", err)
	}
	var auth AuthConfig
	if err := json.Unmarshal(content, &auth); err != nil {
		return AuthConfig{}, fmt.Errorf("decode auth config: %w", err)
	}
	return auth, nil
}

func authConfigFromSession(apiURL string, session nanoflare.AuthSession, preferredOrgID string) (AuthConfig, error) {
	if strings.TrimSpace(session.Token) == "" {
		return AuthConfig{}, errors.New("auth session is missing token")
	}
	auth := AuthConfig{
		APIURL:       apiURL,
		Token:        session.Token,
		RefreshToken: session.RefreshToken,
		ActiveOrgID:  session.ActiveOrgID,
		User:         session.User,
		Orgs:         session.Organizations,
	}
	if preferredOrgID != "" {
		for _, org := range auth.Orgs {
			if org.ID == preferredOrgID {
				auth.ActiveOrgID = preferredOrgID
				break
			}
		}
	}
	if auth.ActiveOrgID == "" && len(auth.Orgs) > 0 {
		auth.ActiveOrgID = auth.Orgs[0].ID
	}
	return auth, nil
}

func writeAuthConfig(auth AuthConfig) error {
	path, err := authConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	content, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return os.WriteFile(path, content, 0o600)
}

var openBrowserFunc = openBrowser

func openBrowser(target string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command = "open"
		args = []string{target}
	case "windows":
		command = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", target}
	default:
		command = "xdg-open"
		args = []string{target}
	}
	return exec.Command(command, args...).Start()
}

func projectAPIURL(project Project, override string) string {
	if override != "" {
		return strings.TrimRight(override, "/")
	}
	if value := os.Getenv("NANOFLARED_URL"); value != "" {
		return strings.TrimRight(value, "/")
	}
	if project.APIURL != "" {
		return strings.TrimRight(project.APIURL, "/")
	}
	return defaultAPIURL
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func sqlFromFlags(command, file string) (string, error) {
	command = strings.TrimSpace(command)
	file = strings.TrimSpace(file)
	if (command == "") == (file == "") {
		return "", errors.New("exactly one of --command or --file is required")
	}
	if command != "" {
		return command, nil
	}
	content, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func migrationFilename(now time.Time, name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var out strings.Builder
	for _, char := range name {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			out.WriteRune(char)
			continue
		}
		if out.Len() > 0 && !strings.HasSuffix(out.String(), "_") {
			out.WriteByte('_')
		}
	}
	slug := strings.Trim(out.String(), "_")
	if slug == "" {
		slug = "migration"
	}
	return now.Format("20060102150405") + "_" + slug + ".sql"
}

func withoutWorkerNoun(args []string) []string {
	if len(args) > 0 && args[0] == "worker" {
		return args[1:]
	}
	return args
}

func (r *Runner) secretPut(args []string) error {
	flags := flag.NewFlagSet("secret put", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", "", "nanoflared base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return errors.New("usage: nanoflare secret put [flags] <name> <value>")
	}
	_, project, err := loadProject()
	if err != nil {
		return err
	}
	if project.AppID == "" {
		return errors.New("worker is not registered; run `nanoflare create` first")
	}
	secretValue := flags.Arg(1)
	if secretValue == "" {
		return errors.New("secret value is required")
	}
	baseURL := projectAPIURL(project, *apiURL)
	if err := r.request(http.MethodPut, baseURL+"/v1/apps/"+project.AppID+"/secrets/"+url.PathEscape(flags.Arg(0)), nanoflare.PutSecretInput{Value: secretValue}, nil); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Updated secret %s\n", flags.Arg(0))
	return nil
}

func (r *Runner) secretList(args []string) error {
	flags := flag.NewFlagSet("secret list", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", "", "nanoflared base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: nanoflare secret list [flags]")
	}
	_, project, err := loadProject()
	if err != nil {
		return err
	}
	if project.AppID == "" {
		return errors.New("worker is not registered; run `nanoflare create` first")
	}
	var secrets []nanoflare.Secret
	baseURL := projectAPIURL(project, *apiURL)
	if err := r.request(http.MethodGet, baseURL+"/v1/apps/"+project.AppID+"/secrets", nil, &secrets); err != nil {
		return err
	}
	for _, secret := range secrets {
		fmt.Fprintf(r.Stdout, "%s\t%s\n", secret.Name, secret.UpdatedAt.Format(time.RFC3339))
	}
	return nil
}

func (r *Runner) secretDelete(args []string) error {
	flags := flag.NewFlagSet("secret delete", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", "", "nanoflared base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: nanoflare secret delete [flags] <name>")
	}
	_, project, err := loadProject()
	if err != nil {
		return err
	}
	if project.AppID == "" {
		return errors.New("worker is not registered; run `nanoflare create` first")
	}
	baseURL := projectAPIURL(project, *apiURL)
	if err := r.request(http.MethodDelete, baseURL+"/v1/apps/"+project.AppID+"/secrets/"+url.PathEscape(flags.Arg(0)), nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Deleted secret %s\n", flags.Arg(0))
	return nil
}

func (r *Runner) secret(args []string) error {
	if len(args) == 0 {
		r.usage()
		return errors.New("secret command is required")
	}
	switch args[0] {
	case "put":
		return r.secretPut(args[1:])
	case "list":
		return r.secretList(args[1:])
	case "delete":
		return r.secretDelete(args[1:])
	default:
		r.usage()
		return fmt.Errorf("unknown secret command %q", args[0])
	}
}

func (r *Runner) kv(args []string) error {
	if len(args) == 0 {
		r.usage()
		return errors.New("kv command is required")
	}
	switch args[0] {
	case "namespace":
		return r.kvNamespace(args[1:])
	default:
		r.usage()
		return fmt.Errorf("unknown kv command %q", args[0])
	}
}

func (r *Runner) db(args []string) error {
	if len(args) == 0 {
		r.usage()
		return errors.New("db command is required")
	}
	switch args[0] {
	case "create":
		return r.dbCreate(args[1:])
	case "list":
		return r.dbList(args[1:])
	case "delete":
		return r.dbDelete(args[1:])
	case "execute":
		return r.dbExecute(args[1:])
	case "migrations":
		return r.dbMigrations(args[1:])
	default:
		r.usage()
		return fmt.Errorf("unknown db command %q", args[0])
	}
}

func (r *Runner) dbCreate(args []string) error {
	flags := flag.NewFlagSet("db create", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", envOrDefault("NANOFLARED_URL", defaultAPIURL), "nanoflared base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: nanoflare db create [flags] <name>")
	}
	var database nanoflare.Database
	if err := r.request(http.MethodPost, strings.TrimRight(*apiURL, "/")+"/v1/db", nanoflare.CreateDatabaseInput{Name: flags.Arg(0)}, &database); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Created database %s\t%s\n", database.ID, database.Name)
	return nil
}

func (r *Runner) dbList(args []string) error {
	flags := flag.NewFlagSet("db list", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", envOrDefault("NANOFLARED_URL", defaultAPIURL), "nanoflared base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: nanoflare db list [flags]")
	}
	var databases []nanoflare.Database
	if err := r.request(http.MethodGet, strings.TrimRight(*apiURL, "/")+"/v1/db", nil, &databases); err != nil {
		return err
	}
	for _, database := range databases {
		fmt.Fprintf(r.Stdout, "%s\t%s\n", database.ID, database.Name)
	}
	return nil
}

func (r *Runner) dbDelete(args []string) error {
	flags := flag.NewFlagSet("db delete", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", envOrDefault("NANOFLARED_URL", defaultAPIURL), "nanoflared base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: nanoflare db delete [flags] <database-id>")
	}
	databaseID := strings.TrimSpace(flags.Arg(0))
	if err := r.request(http.MethodDelete, strings.TrimRight(*apiURL, "/")+"/v1/db/"+url.PathEscape(databaseID), nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Deleted database %s\n", databaseID)
	return nil
}

func (r *Runner) dbExecute(args []string) error {
	flags := flag.NewFlagSet("db execute", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", envOrDefault("NANOFLARED_URL", defaultAPIURL), "nanoflared base URL")
	command := flags.String("command", "", "SQL command to execute")
	file := flags.String("file", "", "SQL file to execute")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: nanoflare db execute [flags] <database-id>")
	}
	sqlText, err := sqlFromFlags(*command, *file)
	if err != nil {
		return err
	}
	var response nanoflare.DBQueryResponse
	endpoint := strings.TrimRight(*apiURL, "/") + "/v1/db/" + url.PathEscape(flags.Arg(0)) + "/execute"
	if err := r.request(http.MethodPost, endpoint, map[string]string{"sql": sqlText}, &response); err != nil {
		return err
	}
	if response.Exec != nil {
		fmt.Fprintf(r.Stdout, "Executed %d statement(s) in %.0fms\n", response.Exec.Count, response.Exec.Duration)
		return nil
	}
	_ = json.NewEncoder(r.Stdout).Encode(response)
	return nil
}

func (r *Runner) dbMigrations(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: nanoflare db migrations <create|apply>")
	}
	switch args[0] {
	case "create":
		return r.dbMigrationsCreate(args[1:])
	case "apply":
		return r.dbMigrationsApply(args[1:])
	default:
		return fmt.Errorf("unknown db migrations command %q", args[0])
	}
}

func (r *Runner) dbMigrationsCreate(args []string) error {
	flags := flag.NewFlagSet("db migrations create", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	pathFlag := flags.String("path", "migrations", "migrations directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: nanoflare db migrations create [flags] <name>")
	}
	if err := os.MkdirAll(*pathFlag, 0o755); err != nil {
		return err
	}
	name := migrationFilename(time.Now().UTC(), flags.Arg(0))
	fullPath := filepath.Join(*pathFlag, name)
	if err := os.WriteFile(fullPath, []byte("-- Write your SQL migration here.\n"), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Created migration %s\n", fullPath)
	return nil
}

func (r *Runner) dbMigrationsApply(args []string) error {
	flags := flag.NewFlagSet("db migrations apply", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", envOrDefault("NANOFLARED_URL", defaultAPIURL), "nanoflared base URL")
	pathFlag := flags.String("path", "migrations", "migrations directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: nanoflare db migrations apply [flags] <database-id>")
	}
	entries, err := os.ReadDir(*pathFlag)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(*apiURL, "/") + "/v1/db/" + url.PathEscape(flags.Arg(0)) + "/migrations"
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(*pathFlag, entry.Name()))
		if err != nil {
			return err
		}
		var result nanoflare.DBMigrationResult
		if err := r.request(http.MethodPost, endpoint, map[string]string{"name": entry.Name(), "sql": string(content)}, &result); err != nil {
			return err
		}
		if result.Applied {
			fmt.Fprintf(r.Stdout, "Applied %s\n", result.Name)
		} else {
			fmt.Fprintf(r.Stdout, "Skipped %s\n", result.Name)
		}
	}
	return nil
}

func (r *Runner) objectStorage(args []string) error {
	if len(args) == 0 {
		r.usage()
		return errors.New("object-storage command is required")
	}
	switch args[0] {
	case "bucket":
		return r.objectStorageBucket(args[1:])
	default:
		r.usage()
		return fmt.Errorf("unknown object-storage command %q", args[0])
	}
}

func (r *Runner) objectStorageBucket(args []string) error {
	if len(args) == 0 {
		r.usage()
		return errors.New("object-storage bucket command is required")
	}
	switch args[0] {
	case "create":
		return r.objectStorageBucketCreate(args[1:])
	case "list":
		return r.objectStorageBucketList(args[1:])
	case "delete":
		return r.objectStorageBucketDelete(args[1:])
	default:
		r.usage()
		return fmt.Errorf("unknown object-storage bucket command %q", args[0])
	}
}

func (r *Runner) kvNamespace(args []string) error {
	if len(args) == 0 {
		r.usage()
		return errors.New("kv namespace command is required")
	}
	switch args[0] {
	case "create":
		return r.kvNamespaceCreate(args[1:])
	case "list":
		return r.kvNamespaceList(args[1:])
	case "delete":
		return r.kvNamespaceDelete(args[1:])
	default:
		r.usage()
		return fmt.Errorf("unknown kv namespace command %q", args[0])
	}
}

func (r *Runner) kvNamespaceCreate(args []string) error {
	flags := flag.NewFlagSet("kv namespace create", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", envOrDefault("NANOFLARED_URL", defaultAPIURL), "nanoflared base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: nanoflare kv namespace create [flags] <name>")
	}
	var namespace nanoflare.KVNamespace
	if err := r.request(http.MethodPost, strings.TrimRight(*apiURL, "/")+"/v1/kv/namespaces", nanoflare.CreateKVNamespaceInput{
		Name: flags.Arg(0),
	}, &namespace); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Created KV namespace %s\t%s\n", namespace.ID, namespace.Name)
	return nil
}

func (r *Runner) kvNamespaceList(args []string) error {
	flags := flag.NewFlagSet("kv namespace list", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", envOrDefault("NANOFLARED_URL", defaultAPIURL), "nanoflared base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: nanoflare kv namespace list [flags]")
	}
	var namespaces []nanoflare.KVNamespace
	if err := r.request(http.MethodGet, strings.TrimRight(*apiURL, "/")+"/v1/kv/namespaces", nil, &namespaces); err != nil {
		return err
	}
	for _, namespace := range namespaces {
		fmt.Fprintf(r.Stdout, "%s\t%s\n", namespace.ID, namespace.Name)
	}
	return nil
}

func (r *Runner) kvNamespaceDelete(args []string) error {
	flags := flag.NewFlagSet("kv namespace delete", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", envOrDefault("NANOFLARED_URL", defaultAPIURL), "nanoflared base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: nanoflare kv namespace delete [flags] <namespace-id>")
	}
	namespaceID := strings.TrimSpace(flags.Arg(0))
	if namespaceID == "" {
		return errors.New("namespace id is required")
	}
	if err := r.request(http.MethodDelete, strings.TrimRight(*apiURL, "/")+"/v1/kv/namespaces/"+namespaceID, nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Deleted KV namespace %s\n", namespaceID)
	return nil
}

func (r *Runner) objectStorageBucketCreate(args []string) error {
	flags := flag.NewFlagSet("object-storage bucket create", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", envOrDefault("NANOFLARED_URL", defaultAPIURL), "nanoflared base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: nanoflare object-storage bucket create [flags] <name>")
	}
	var bucket nanoflare.ObjectStorageBucket
	if err := r.request(http.MethodPost, strings.TrimRight(*apiURL, "/")+"/v1/object-storage-buckets", nanoflare.CreateObjectStorageBucketInput{
		Name: flags.Arg(0),
	}, &bucket); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Created object storage bucket %s\t%s\n", bucket.ID, bucket.Name)
	return nil
}

func (r *Runner) objectStorageBucketList(args []string) error {
	flags := flag.NewFlagSet("object-storage bucket list", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", envOrDefault("NANOFLARED_URL", defaultAPIURL), "nanoflared base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: nanoflare object-storage bucket list [flags]")
	}
	var buckets []nanoflare.ObjectStorageBucket
	if err := r.request(http.MethodGet, strings.TrimRight(*apiURL, "/")+"/v1/object-storage-buckets", nil, &buckets); err != nil {
		return err
	}
	for _, bucket := range buckets {
		fmt.Fprintf(r.Stdout, "%s\t%s\n", bucket.ID, bucket.Name)
	}
	return nil
}

func (r *Runner) objectStorageBucketDelete(args []string) error {
	flags := flag.NewFlagSet("object-storage bucket delete", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", envOrDefault("NANOFLARED_URL", defaultAPIURL), "nanoflared base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: nanoflare object-storage bucket delete [flags] <bucket-id>")
	}
	bucketID := strings.TrimSpace(flags.Arg(0))
	if bucketID == "" {
		return errors.New("bucket id is required")
	}
	if err := r.request(http.MethodDelete, strings.TrimRight(*apiURL, "/")+"/v1/object-storage-buckets/"+bucketID, nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Deleted object storage bucket %s\n", bucketID)
	return nil
}

func slug(value string) string {
	var result strings.Builder
	dash := false
	for _, char := range strings.ToLower(value) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			result.WriteRune(char)
			dash = false
		} else if result.Len() > 0 && !dash {
			result.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(result.String(), "-")
}

func (r *Runner) usage() {
	fmt.Fprintln(r.Stderr, `Usage:
  nanoflare init [flags] [directory]
  nanoflare create [worker] [flags]
  nanoflare list [worker] [flags]
  nanoflare delete [worker] [app-id] [flags]
  nanoflare deploy [worker] [flags]
  nanoflare auth login [flags]
  nanoflare auth orgs
  nanoflare auth use-org <org-id>
  nanoflare auth whoami
  nanoflare auth logout
  nanoflare secret put [flags] <name> <value>
  nanoflare secret list [flags]
  nanoflare secret delete [flags] <name>
  nanoflare kv namespace create [flags] <name>
  nanoflare kv namespace list [flags]
  nanoflare kv namespace delete [flags] <namespace-id>
  nanoflare db create [flags] <name>
  nanoflare db list [flags]
  nanoflare db delete [flags] <database-id>
  nanoflare db execute [flags] <database-id>
  nanoflare db migrations create [flags] <name>
  nanoflare db migrations apply [flags] <database-id>
  nanoflare object-storage bucket create [flags] <name>
  nanoflare object-storage bucket list [flags]
  nanoflare object-storage bucket delete [flags] <bucket-id>`)
}
