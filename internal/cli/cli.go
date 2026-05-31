package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/clas/platform/internal/platform"
	starterworker "github.com/clas/platform/templates/starter-worker"
)

const (
	projectFilename = "platform.json"
	defaultAPIURL   = "http://127.0.0.1:8080"
)

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type Runner struct {
	Client HTTPClient
	Stdout io.Writer
	Stderr io.Writer
	Now    func() time.Time
}

type Project struct {
	Name              string   `json:"name"`
	Hostname          string   `json:"hostname"`
	AppID             string   `json:"app_id,omitempty"`
	APIURL            string   `json:"api_url"`
	Entrypoint        string   `json:"entrypoint"`
	Format            string   `json:"format,omitempty"`
	CompatibilityDate string   `json:"compatibility_date"`
	Files             []string `json:"files"`
}

func NewRunner(stdout, stderr io.Writer) *Runner {
	return &Runner{
		Client: http.DefaultClient,
		Stdout: stdout,
		Stderr: stderr,
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
	case "deploy":
		return r.deploy(withoutWorkerNoun(args[1:]))
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
	apiURL := flags.String("api-url", envOrDefault("PLATFORMD_URL", defaultAPIURL), "platformd base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("usage: platform init [flags] [directory]")
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
	if projectHostname == "" {
		projectSlug := slug(projectName)
		if projectSlug == "" {
			projectSlug = "worker"
		}
		projectHostname = projectSlug + ".example.com"
	}
	project := Project{
		Name:              projectName,
		Hostname:          projectHostname,
		APIURL:            strings.TrimRight(*apiURL, "/"),
		Entrypoint:        "worker.js",
		Format:            "modules",
		CompatibilityDate: "2025-12-10",
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
	fmt.Fprintln(r.Stdout, "Run `platform create` to register it, then `platform deploy`.")
	return nil
}

func (r *Runner) create(args []string) error {
	flags := flag.NewFlagSet("create", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", "", "platformd base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: platform create [worker] [flags]")
	}
	path, project, err := loadProject()
	if err != nil {
		return err
	}
	if project.AppID != "" {
		return fmt.Errorf("worker is already registered as %s", project.AppID)
	}
	baseURL := projectAPIURL(project, *apiURL)
	var app platform.App
	if err := r.request(http.MethodPost, baseURL+"/v1/apps", platform.CreateAppInput{
		Name:     project.Name,
		Hostname: project.Hostname,
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

func (r *Runner) deploy(args []string) error {
	flags := flag.NewFlagSet("deploy", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	apiURL := flags.String("api-url", "", "platformd base URL")
	compatibilityDate := flags.String("compatibility-date", "", "worker compatibility date (YYYY-MM-DD)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: platform deploy [worker] [flags]")
	}
	_, project, err := loadProject()
	if err != nil {
		return err
	}
	if project.AppID == "" {
		return errors.New("worker is not registered; run `platform create` first")
	}
	date := project.CompatibilityDate
	if *compatibilityDate != "" {
		date = *compatibilityDate
	}
	files, err := loadWorkerFiles(project.Files)
	if err != nil {
		return err
	}
	var deployment platform.Deployment
	if err := r.request(http.MethodPost, projectAPIURL(project, *apiURL)+"/v1/apps/"+project.AppID+"/deployments", platform.DeployInput{
		Files:             files,
		Entrypoint:        project.Entrypoint,
		Format:            project.Format,
		CompatibilityDate: date,
	}, &deployment); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Deployed worker %s as deployment %s\n", project.AppID, deployment.ID)
	return nil
}

func (r *Runner) request(method, url string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
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
			return fmt.Errorf("%s %s: platformd returned %s", method, url, response.Status)
		}
		return fmt.Errorf("%s %s: %s", method, url, apiError.Error)
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("decode platformd response: %w", err)
	}
	return nil
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
	if project.Name == "" || project.Hostname == "" || project.Entrypoint == "" || project.CompatibilityDate == "" || len(project.Files) == 0 {
		return "", Project{}, fmt.Errorf("%s is missing required worker configuration", path)
	}
	return path, project, nil
}

func loadWorkerFiles(paths []string) ([]platform.WorkerFile, error) {
	files := make([]platform.WorkerFile, 0, len(paths))
	for _, path := range paths {
		clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(path)))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("worker file path %q must remain inside the project", path)
		}
		content, err := os.ReadFile(clean)
		if err != nil {
			return nil, fmt.Errorf("read worker file %s: %w", clean, err)
		}
		files = append(files, platform.WorkerFile{Path: filepath.ToSlash(clean), Content: string(content)})
	}
	return files, nil
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

func projectAPIURL(project Project, override string) string {
	if override != "" {
		return strings.TrimRight(override, "/")
	}
	if value := os.Getenv("PLATFORMD_URL"); value != "" {
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

func withoutWorkerNoun(args []string) []string {
	if len(args) > 0 && args[0] == "worker" {
		return args[1:]
	}
	return args
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
  platform init [flags] [directory]
  platform create [worker] [flags]
  platform deploy [worker] [flags]`)
}
