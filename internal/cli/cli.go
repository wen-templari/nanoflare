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
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/templates"
	"github.com/mattn/go-isatty"
)

const (
	projectFilename  = "nanoflare.json"
	defaultAPIURL    = "http://127.0.0.1:8080"
	authFilename     = "auth.json"
	authStorePathEnv = "NANOFLARE_AUTH_STORE"
	authTokenEnv     = "NANOFLARE_TOKEN"
	authOrgIDEnv     = "NANOFLARE_ORG_ID"
)

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type Runner struct {
	Client        HTTPClient
	Stdout        io.Writer
	Stderr        io.Writer
	Stdin         io.Reader
	Now           func() time.Time
	IsInteractive func() bool
	Templates     []templates.Template
}

type Project struct {
	Name                 string                                 `json:"name"`
	Main                 string                                 `json:"main"`
	Format               string                                 `json:"format,omitempty"`
	CompatibilityDate    string                                 `json:"compatibility_date"`
	CompatibilityFlags   []string                               `json:"compatibility_flags,omitempty"`
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
	Entrypoint                string                             `json:"entrypoint,omitempty"`
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
		IsInteractive: func() bool {
			return isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
		},
		Templates: templates.Catalog,
	}
}

func (r *Runner) Run(args []string) error {
	command := r.newRootCommand()
	command.SetArgs(args)
	return command.Execute()
}

func (r *Runner) init(args []string) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	name := flags.String("name", "", "worker name")
	templateID := flags.String("template", "", "template to initialize")
	listTemplates := flags.Bool("list-templates", false, "list available templates")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *listTemplates {
		if strings.TrimSpace(*templateID) != "" {
			return errors.New("--list-templates cannot be used with --template")
		}
		if flags.NArg() != 0 {
			return errors.New("usage: nanoflare init --list-templates")
		}
		r.printTemplates()
		return nil
	}
	if flags.NArg() > 1 {
		return errors.New("usage: nanoflare init [flags] [directory]")
	}
	template, err := r.selectTemplate(strings.TrimSpace(*templateID))
	if err != nil {
		return err
	}
	dir := "."
	if flags.NArg() == 1 {
		dir = flags.Arg(0)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := requireEmptyDirectory(absDir); err != nil {
		return err
	}
	files, err := templateFiles(template)
	if err != nil {
		return fmt.Errorf("read template %q: %w", template.ID, err)
	}
	projectName := strings.TrimSpace(*name)
	if projectName == "" {
		projectName = filepath.Base(absDir)
	}
	projectConfig := template.Project()
	project := Project{
		Name:              projectName,
		Main:              projectConfig.Main,
		Format:            projectConfig.Format,
		CompatibilityDate: r.Now().UTC().Format("2006-01-02"),
		Files:             append([]string(nil), projectConfig.Files...),
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return fmt.Errorf("create project directory: %w", err)
	}
	for _, file := range files {
		path := filepath.Join(absDir, filepath.FromSlash(file.path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create template directory: %w", err)
		}
		if err := os.WriteFile(path, file.data, 0o644); err != nil {
			return fmt.Errorf("write template file %s: %w", file.path, err)
		}
	}
	if err := writeProject(filepath.Join(absDir, projectFilename), project, os.O_EXCL); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Initialized worker project in %s\n", absDir)
	fmt.Fprintln(r.Stdout, "Run `nanoflare create` to register it, then `nanoflare deploy`.")
	return nil
}

type templateFile struct {
	path string
	data []byte
}

func (r *Runner) templateCatalog() []templates.Template {
	if r.Templates != nil {
		return r.Templates
	}
	return templates.Catalog
}

func (r *Runner) printTemplates() {
	writer := tabwriter.NewWriter(r.Stdout, 0, 4, 2, ' ', 0)
	for _, template := range r.templateCatalog() {
		fmt.Fprintf(writer, "%s\t%s\n", template.ID, template.Description)
	}
	_ = writer.Flush()
}

func (r *Runner) selectTemplate(id string) (templates.Template, error) {
	catalog := r.templateCatalog()
	if len(catalog) == 0 {
		return templates.Template{}, errors.New("no project templates are available")
	}
	if id != "" {
		return findTemplate(catalog, id)
	}
	if r.IsInteractive == nil || !r.IsInteractive() {
		return catalog[0], nil
	}
	reader := bufio.NewReader(r.Stdin)
	for {
		fmt.Fprintln(r.Stderr, "Available templates:")
		for index, template := range catalog {
			fmt.Fprintf(r.Stderr, "  %d) %s — %s\n", index+1, template.ID, template.Description)
		}
		fmt.Fprintf(r.Stderr, "Select a template [%s]: ", catalog[0].ID)
		value, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return templates.Template{}, err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return catalog[0], nil
		}
		if index, parseErr := strconv.Atoi(value); parseErr == nil && index >= 1 && index <= len(catalog) {
			return catalog[index-1], nil
		}
		if template, findErr := findTemplate(catalog, value); findErr == nil {
			return template, nil
		}
		fmt.Fprintf(r.Stderr, "Unknown template %q. Choose a listed number or ID.\n", value)
		if errors.Is(err, io.EOF) {
			return templates.Template{}, fmt.Errorf("unknown template %q", value)
		}
	}
}

func findTemplate(catalog []templates.Template, id string) (templates.Template, error) {
	for _, template := range catalog {
		if template.ID == id {
			return template, nil
		}
	}
	ids := make([]string, 0, len(catalog))
	for _, template := range catalog {
		ids = append(ids, template.ID)
	}
	return templates.Template{}, fmt.Errorf("unknown template %q (available: %s)", id, strings.Join(ids, ", "))
}

func requireEmptyDirectory(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("project destination %s is not a directory", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("project destination %s is not empty", path)
	}
	return nil
}

func templateFiles(template templates.Template) ([]templateFile, error) {
	if template.Files == nil || template.Root == "" {
		return nil, errors.New("template has no embedded files")
	}
	files := make([]templateFile, 0)
	err := fs.WalkDir(template.Files, template.Root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, ok := strings.CutPrefix(path, strings.TrimSuffix(template.Root, "/")+"/")
		if !ok {
			return fmt.Errorf("template file %q is outside root %q", path, template.Root)
		}
		if err := validateTemplatePath(relative); err != nil {
			return err
		}
		data, err := fs.ReadFile(template.Files, path)
		if err != nil {
			return err
		}
		files = append(files, templateFile{path: relative, data: data})
		return nil
	})
	return files, err
}

func validateTemplatePath(path string) error {
	if path == projectFilename || path == "." || !fs.ValidPath(path) || filepath.IsAbs(path) {
		return fmt.Errorf("unsafe template path %q", path)
	}
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
	_, project, err := loadProject()
	if err != nil {
		return err
	}
	baseURL := projectAPIURL(*apiURL)
	if _, err := r.projectApp(baseURL, project.Name); err == nil {
		return fmt.Errorf("worker %q already exists", project.Name)
	} else if !errors.Is(err, errProjectAppNotFound) {
		return err
	}
	var app nanoflare.App
	if err := r.request(http.MethodPost, baseURL+"/v1/workers", nanoflare.CreateAppInput{
		Name: project.Name,
		Auth: nanoflare.AuthConfig{ProtectedRoutes: append([]string(nil), project.Auth.ProtectedRoutes...)},
	}, &app); err != nil {
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
	if err := r.request(http.MethodGet, strings.TrimRight(*apiURL, "/")+"/v1/workers", nil, &apps); err != nil {
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
		return errors.New("usage: nanoflare delete [worker] [worker-id] [flags]")
	}
	appID := ""
	var project Project
	if flags.NArg() == 1 {
		appID = strings.TrimSpace(flags.Arg(0))
	} else {
		var err error
		_, project, err = loadProject()
		if err != nil {
			return err
		}
		baseURL := projectAPIURL(*apiURL)
		app, err := r.projectApp(baseURL, project.Name)
		if err != nil {
			return err
		}
		appID = app.ID
	}
	baseURL := projectAPIURL(*apiURL)
	if err := r.request(http.MethodDelete, baseURL+"/v1/workers/"+appID, nil, nil); err != nil {
		return err
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
	date := project.CompatibilityDate
	if *compatibilityDate != "" {
		date = *compatibilityDate
	}
	baseURL := projectAPIURL(*apiURL)
	app, err := r.projectApp(baseURL, project.Name)
	if err != nil {
		return err
	}
	if err := r.request(http.MethodPatch, baseURL+"/v1/workers/"+app.ID, nanoflare.UpdateAppInput{
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
	if err := r.request(http.MethodPost, baseURL+"/v1/workers/"+app.ID+"/deployments", nanoflare.DeployInput{
		CommitHash:           commitHash,
		CommitMessage:        commitMessage,
		Files:                files,
		Assets:               assets,
		Entrypoint:           project.Main,
		Format:               project.Format,
		CompatibilityDate:    date,
		CompatibilityFlags:   append([]string(nil), project.CompatibilityFlags...),
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
	if deployment.CompatibilityDate != date {
		fmt.Fprintf(r.Stderr, "Warning: compatibility date %s is not supported by the server; using %s instead\n", date, deployment.CompatibilityDate)
	}
	fmt.Fprintf(r.Stdout, "Deployed worker %s as deployment %s\n", app.ID, deployment.ID)
	if hostname := strings.TrimSpace(app.Hostname); hostname != "" {
		fmt.Fprintf(r.Stdout, "Worker URL: https://%s\n", hostname)
	}
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

func (r *Runner) deployment(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: nanoflare deployment <output>")
	}
	switch args[0] {
	case "output":
		return r.deploymentOutput(args[1:])
	default:
		return fmt.Errorf("unknown deployment command %q", args[0])
	}
}

func (r *Runner) deploymentOutput(args []string) error {
	flags := flag.NewFlagSet("deployment output", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", "", "nanoflared base URL")
	deploymentID := flags.String("deployment", "", "deployment ID")
	level := flags.String("level", "", "output level")
	search := flags.String("search", "", "text to search for")
	limit := flags.Int("limit", 500, "maximum output lines (1-1000)")
	since := flags.String("since", "", "RFC3339 start time")
	until := flags.String("until", "", "RFC3339 end time")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("usage: nanoflare deployment output [worker-id] [flags]")
	}
	var project Project
	appID := ""
	if flags.NArg() == 1 {
		appID = strings.TrimSpace(flags.Arg(0))
	} else {
		var err error
		_, project, err = loadProject()
		if err != nil {
			return err
		}
		app, err := r.projectApp(projectAPIURL(*apiURL), project.Name)
		if err != nil {
			return err
		}
		appID = app.ID
	}
	if appID == "" {
		return errors.New("app id is required")
	}
	baseURL := projectAPIURL(*apiURL)
	query := url.Values{}
	if *deploymentID != "" {
		query.Set("deployment_id", *deploymentID)
	}
	if *level != "" {
		query.Set("level", *level)
	}
	if *search != "" {
		query.Set("q", *search)
	}
	if *since != "" {
		query.Set("since", *since)
	}
	if *until != "" {
		query.Set("until", *until)
	}
	if *limit != 500 {
		query.Set("limit", strconv.Itoa(*limit))
	}
	endpoint := baseURL + "/v1/workers/" + appID + "/output"
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	var output []nanoflare.WorkerOutputLine
	if err := r.request(http.MethodGet, endpoint, nil, &output); err != nil {
		return err
	}
	for _, line := range output {
		fmt.Fprintf(r.Stdout, "%s\t%s\t%s\n", line.Timestamp.UTC().Format(time.RFC3339), line.Level, line.Message)
	}
	return nil
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
	webLogin := flags.Bool("web", false, "use browser login flow")
	patLogin := flags.Bool("pat", false, "use personal access token login flow")
	patToken := flags.String("pat-token", "", "personal access token for non-interactive login")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("usage: nanoflare auth login [--web | --pat [token] | --pat-token token]")
	}
	baseURL := strings.TrimRight(*apiURL, "/")
	token := strings.TrimSpace(*patToken)
	if flags.NArg() == 1 {
		if !*patLogin && token == "" {
			return errors.New("personal access token argument requires --pat")
		}
		if token != "" {
			return errors.New("pass the personal access token either as an argument or with --pat-token, not both")
		}
		token = strings.TrimSpace(flags.Arg(0))
		*patLogin = true
	}
	if *webLogin && (*patLogin || token != "") {
		return errors.New("--web cannot be used with --pat or --pat-token")
	}
	if token != "" {
		*patLogin = true
	}
	reader := bufio.NewReader(r.Stdin)
	if !*webLogin && !*patLogin {
		method, err := r.promptAuthLoginMethod(reader)
		if err != nil {
			return err
		}
		switch method {
		case "web":
			*webLogin = true
		case "pat":
			*patLogin = true
		default:
			return fmt.Errorf("unknown login method %q", method)
		}
	}
	if *webLogin {
		return r.authLoginWeb(baseURL)
	}
	return r.authLoginPAT(baseURL, token, reader)
}

func (r *Runner) promptAuthLoginMethod(reader *bufio.Reader) (string, error) {
	for {
		fmt.Fprint(r.Stderr, "Login method (web/pat) [web]: ")
		value, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		method := strings.ToLower(strings.TrimSpace(value))
		if method == "" {
			return "web", nil
		}
		if method == "web" || method == "pat" {
			return method, nil
		}
		fmt.Fprintln(r.Stderr, "Choose web or pat.")
		if errors.Is(err, io.EOF) {
			return "", errors.New("login method must be web or pat")
		}
	}
}

func (r *Runner) authLoginPAT(baseURL string, token string, reader *bufio.Reader) error {
	token = strings.TrimSpace(token)
	if token == "" {
		fmt.Fprint(r.Stderr, "Personal access token: ")
		value, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		token = strings.TrimSpace(value)
	}
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
	_, _ = io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Nanoflare CLI login</title><style>:root{color:#1d1d1d;background:#f7f7f8;font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}*{box-sizing:border-box}body{margin:0;min-height:100vh;padding:2rem 1.25rem;display:grid;place-items:center}.shell{width:min(100%,520px)}.heading{display:flex;align-items:center;gap:.75rem;margin-bottom:1.5rem}.icon{width:2.5rem;height:2.5rem;border-radius:.5rem;display:grid;place-items:center;background:#e5f2ff;color:#0059b3;font-size:1.25rem;font-weight:600}.title{margin:0;font-size:1.125rem;line-height:1.4;font-weight:600}.subtitle{margin:.125rem 0 0;color:#5c5f66;font-size:.875rem}.card{background:#fff;border:1px solid #d9d9de;border-radius:.5rem;padding:1rem 1.25rem}.status{display:flex;align-items:flex-start;gap:.75rem}.check{width:1.25rem;height:1.25rem;flex:0 0 auto;margin-top:.0625rem;border-radius:50%;display:grid;place-items:center;background:#e4f7ec;color:#16794c;font-size:.8125rem;font-weight:600}.message{margin:0;font-size:.875rem;line-height:1.5}.hint{margin:.25rem 0 0;color:#5c5f66;font-size:.875rem;line-height:1.5}@media(min-width:768px){body{padding:2.5rem 2rem}}</style></head><body><main class="shell"><header class="heading"><div class="icon" aria-hidden="true">⌘</div><div><h1 class="title">Nanoflare CLI login</h1><p class="subtitle">Login complete</p></div></header><section class="card"><div class="status"><div class="check" aria-hidden="true">✓</div><div><p class="message">You’re signed in to the Nanoflare CLI.</p><p class="hint">You can close this tab and return to your terminal.</p></div></div></section></main></body></html>`)
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
	auth, _, err := resolveAuthConfig()
	if err != nil {
		return err
	}
	if environmentCredentialsConfigured() {
		if auth.ActiveOrgID != "" {
			fmt.Fprintf(r.Stdout, "* %s\n", auth.ActiveOrgID)
		}
		return nil
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
	if strings.TrimSpace(os.Getenv(authOrgIDEnv)) != "" {
		return fmt.Errorf("cannot change organization while %s is set", authOrgIDEnv)
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
	auth, _, err := resolveAuthConfig()
	if err != nil {
		return err
	}
	if environmentCredentialsConfigured() {
		fmt.Fprintln(r.Stdout, "environment credentials")
		if auth.ActiveOrgID != "" {
			fmt.Fprintf(r.Stdout, "org\t%s\n", auth.ActiveOrgID)
		}
		return nil
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
	auth, authFromEnvironment, authErr := resolveAuthConfig()
	response, err := r.authenticatedRequest(method, url, payload, input != nil, auth)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, url, err)
	}
	if response.StatusCode == http.StatusUnauthorized && !authFromEnvironment && authErr == nil && auth.RefreshToken != "" {
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
	target, err := organizationScopedURL(target, auth.ActiveOrgID)
	if err != nil {
		return nil, err
	}
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
	}
	return r.Client.Do(request)
}

func organizationScopedURL(target, orgID string) (string, error) {
	parsed, err := url.Parse(target)
	if err != nil || strings.TrimSpace(orgID) == "" || strings.HasPrefix(parsed.Path, "/v1/organizations/") {
		return target, err
	}
	for _, prefix := range []string{"/v1/workers", "/v1/kv/namespaces", "/v1/db", "/v1/object-storage-buckets"} {
		if strings.HasPrefix(parsed.Path, prefix) {
			rest := strings.TrimPrefix(parsed.Path, prefix)
			resource := strings.TrimPrefix(prefix, "/v1/")
			switch resource {
			case "kv/namespaces":
				resource = "kv-namespaces"
			case "db":
				resource = "databases"
			}
			if resource == "workers" {
				rest = strings.Replace(rest, "/deployments/traffic", "/deployment-traffic", 1)
				rest = strings.Replace(rest, "/traffic", "/analytics/traffic", 1)
			}
			if resource == "databases" {
				rest = strings.Replace(rest, "/execute", "/queries", 1)
				rest = strings.Replace(rest, "/metrics/timeseries", "/analytics/timeseries", 1)
				rest = strings.Replace(rest, "/metrics", "/analytics", 1)
			}
			parsed.Path = "/v1/organizations/" + url.PathEscape(orgID) + "/" + resource + rest
			return parsed.String(), nil
		}
	}
	return target, nil
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
	project, err := loadProjectAtPath(path, true)
	if err != nil {
		return "", Project{}, err
	}
	return path, project, nil
}

func loadProjectAtPath(path string, migrateAliases bool) (Project, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Project{}, fmt.Errorf("read %s: %w", path, err)
	}
	var project Project
	if err := json.Unmarshal(content, &project); err != nil {
		return Project{}, fmt.Errorf("decode %s: %w", path, err)
	}
	var alias projectAlias
	if err := json.Unmarshal(content, &alias); err != nil {
		return Project{}, fmt.Errorf("decode %s aliases: %w", path, err)
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
	if project.Main == "" && alias.Entrypoint != "" {
		project.Main = alias.Entrypoint
	}
	if migrateAliases && alias.Entrypoint != "" {
		if err := writeProject(path, project, os.O_TRUNC); err != nil {
			return Project{}, fmt.Errorf("migrate %s: %w", path, err)
		}
	}
	if project.Name == "" || project.Main == "" || project.CompatibilityDate == "" || len(project.Files) == 0 {
		return Project{}, fmt.Errorf("%s is missing required worker configuration", path)
	}
	return project, nil
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

// resolveAuthConfig applies the process-level credential overrides to the stored
// login. Its boolean result reports whether at least one environment credential
// was configured, which disables refreshes and auth-store writes for requests.
func resolveAuthConfig() (AuthConfig, bool, error) {
	token := strings.TrimSpace(os.Getenv(authTokenEnv))
	orgID := strings.TrimSpace(os.Getenv(authOrgIDEnv))
	fromEnvironment := token != "" || orgID != ""
	auth, err := loadAuthConfig()
	if err != nil {
		if !fromEnvironment {
			return AuthConfig{}, false, err
		}
		auth = AuthConfig{}
	}
	if token != "" {
		auth.Token = token
		auth.RefreshToken = ""
	}
	if orgID != "" {
		auth.ActiveOrgID = orgID
	}
	return auth, fromEnvironment, nil
}

func environmentCredentialsConfigured() bool {
	return strings.TrimSpace(os.Getenv(authTokenEnv)) != "" || strings.TrimSpace(os.Getenv(authOrgIDEnv)) != ""
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

var errProjectAppNotFound = errors.New("worker not found")

func (r *Runner) projectApp(baseURL, name string) (nanoflare.App, error) {
	var apps []nanoflare.App
	if err := r.request(http.MethodGet, baseURL+"/v1/workers", nil, &apps); err != nil {
		return nanoflare.App{}, err
	}
	var match *nanoflare.App
	for i := range apps {
		if apps[i].Name != name {
			continue
		}
		if match != nil {
			return nanoflare.App{}, fmt.Errorf("multiple workers named %q; use a unique name", name)
		}
		match = &apps[i]
	}
	if match == nil {
		return nanoflare.App{}, fmt.Errorf("%w: %q; run `nanoflare create` first", errProjectAppNotFound, name)
	}
	return *match, nil
}

func projectAPIURL(override string) string {
	if override != "" {
		return strings.TrimRight(override, "/")
	}
	if value := os.Getenv("NANOFLARED_URL"); value != "" {
		return strings.TrimRight(value, "/")
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

func hasOneSQLStatement(sql string) bool {
	statementCount := 0
	inSingleQuote, inDoubleQuote, inBacktick, inBracket := false, false, false, false
	inLineComment, inBlockComment := false, false
	hasContent := false
	for i := 0; i < len(sql); i++ {
		char := sql[i]
		next := byte(0)
		if i+1 < len(sql) {
			next = sql[i+1]
		}
		if inLineComment {
			if char == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if char == '*' && next == '/' {
				inBlockComment = false
				i++
			}
			continue
		}
		if !inSingleQuote && !inDoubleQuote && !inBacktick && !inBracket {
			if char == '-' && next == '-' {
				inLineComment = true
				i++
				continue
			}
			if char == '/' && next == '*' {
				inBlockComment = true
				i++
				continue
			}
		}
		switch char {
		case '\'':
			if !inDoubleQuote && !inBacktick && !inBracket {
				if inSingleQuote && next == '\'' {
					i++
					continue
				}
				inSingleQuote = !inSingleQuote
			}
		case '"':
			if !inSingleQuote && !inBacktick && !inBracket {
				if inDoubleQuote && next == '"' {
					i++
					continue
				}
				inDoubleQuote = !inDoubleQuote
			}
		case '`':
			if !inSingleQuote && !inDoubleQuote && !inBracket {
				inBacktick = !inBacktick
			}
		case '[':
			if !inSingleQuote && !inDoubleQuote && !inBacktick {
				inBracket = true
			}
		case ']':
			if inBracket {
				inBracket = false
			}
		case ';':
			if !inSingleQuote && !inDoubleQuote && !inBacktick && !inBracket && hasContent {
				statementCount++
				hasContent = false
			}
		default:
			if !inSingleQuote && !inDoubleQuote && !inBacktick && !inBracket && !strings.ContainsRune(" \t\r\n", rune(char)) {
				hasContent = true
			}
		}
	}
	if hasContent {
		statementCount++
	}
	return statementCount == 1
}

func writeDBResult(output io.Writer, result nanoflare.D1Result) {
	if len(result.Columns) == 0 {
		fmt.Fprintf(output, "Statement completed in %.0fms", result.Meta.Duration)
		if result.Meta.Changes > 0 {
			fmt.Fprintf(output, "; %d row(s) affected", result.Meta.Changes)
		}
		fmt.Fprintln(output, ".")
		return
	}
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, strings.Join(result.Columns, "\t"))
	for _, row := range result.Results {
		values := make([]string, len(result.Columns))
		for index, column := range result.Columns {
			values[index] = formatDBValue(row[column])
		}
		fmt.Fprintln(writer, strings.Join(values, "\t"))
	}
	_ = writer.Flush()
	if len(result.Results) == 0 {
		fmt.Fprintln(output, "(0 rows)")
	}
}

func formatDBValue(value any) string {
	if value == nil {
		return "NULL"
	}
	text := fmt.Sprint(value)
	text = strings.ReplaceAll(text, "\t", " ")
	return strings.ReplaceAll(text, "\n", "\\n")
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
	secretValue := flags.Arg(1)
	if secretValue == "" {
		return errors.New("secret value is required")
	}
	baseURL := projectAPIURL(*apiURL)
	app, err := r.projectApp(baseURL, project.Name)
	if err != nil {
		return err
	}
	if err := r.request(http.MethodPut, baseURL+"/v1/workers/"+app.ID+"/secrets/"+url.PathEscape(flags.Arg(0)), nanoflare.PutSecretInput{Value: secretValue}, nil); err != nil {
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
	var secrets []nanoflare.Secret
	baseURL := projectAPIURL(*apiURL)
	app, err := r.projectApp(baseURL, project.Name)
	if err != nil {
		return err
	}
	if err := r.request(http.MethodGet, baseURL+"/v1/workers/"+app.ID+"/secrets", nil, &secrets); err != nil {
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
	baseURL := projectAPIURL(*apiURL)
	app, err := r.projectApp(baseURL, project.Name)
	if err != nil {
		return err
	}
	if err := r.request(http.MethodDelete, baseURL+"/v1/workers/"+app.ID+"/secrets/"+url.PathEscape(flags.Arg(0)), nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Deleted secret %s\n", flags.Arg(0))
	return nil
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
	command := flags.String("command", "", "SQL statement to run")
	file := flags.String("file", "", "Path to a file containing one SQL statement")
	jsonOutput := flags.Bool("json", false, "Print the complete response as JSON")
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
	if !hasOneSQLStatement(sqlText) {
		return errors.New("db execute accepts exactly one SQL statement; use migrations for multi-statement schema changes")
	}
	var response nanoflare.DBQueryResponse
	endpoint := strings.TrimRight(*apiURL, "/") + "/v1/db/" + url.PathEscape(flags.Arg(0)) + "/execute"
	input := map[string]any{"statements": []nanoflare.DBStatementRequest{{SQL: sqlText}}}
	if err := r.request(http.MethodPost, endpoint, input, &response); err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(r.Stdout).Encode(response)
	}
	if len(response.Results) > 0 {
		writeDBResult(r.Stdout, response.Results[0])
		return nil
	}
	if response.Exec != nil {
		fmt.Fprintf(r.Stdout, "Executed %d statement(s) in %.0fms\n", response.Exec.Count, response.Exec.Duration)
		return nil
	}
	fmt.Fprintln(r.Stdout, "Statement completed.")
	return nil
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
