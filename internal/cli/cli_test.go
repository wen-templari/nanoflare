package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestInitCreatesStarterProject(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	var stdout bytes.Buffer
	runner := NewRunner(&stdout, io.Discard)
	runner.Now = func() time.Time {
		return time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	}

	if err := runner.Run([]string{"init", "--name", "Hello Worker", "hello"}); err != nil {
		t.Fatal(err)
	}

	project := readProject(t, filepath.Join("hello", projectFilename))
	if project.Name != "Hello Worker" || project.Hostname != "" {
		t.Fatalf("project = %#v", project)
	}
	if project.CompatibilityDate != "2026-05-31" || project.Entrypoint != "worker.js" || project.Format != "modules" {
		t.Fatalf("project = %#v", project)
	}
	content, err := os.ReadFile(filepath.Join("hello", "worker.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "hello from nanoflare") {
		t.Fatalf("starter worker = %q", content)
	}
}

func TestInitPreservesExplicitHostname(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	runner := NewRunner(io.Discard, io.Discard)
	runner.Now = func() time.Time {
		return time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	}

	if err := runner.Run([]string{"init", "--name", "Hello Worker", "--hostname", "hello.example.com", "hello"}); err != nil {
		t.Fatal(err)
	}

	project := readProject(t, filepath.Join("hello", projectFilename))
	if project.Hostname != "hello.example.com" {
		t.Fatalf("hostname = %q", project.Hostname)
	}
}

func TestCreateAndDeployWorker(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	var created nanoflare.CreateAppInput
	var updated nanoflare.UpdateAppInput
	var deployed nanoflare.DeployInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/apps":
			decodeRequest(t, r, &created)
			writeJSON(t, w, http.StatusCreated, nanoflare.App{ID: "app-123", Hostname: created.Hostname})
		case "/v1/apps/app-123":
			if r.Method != http.MethodPatch {
				http.NotFound(w, r)
				return
			}
			decodeRequest(t, r, &updated)
			w.WriteHeader(http.StatusOK)
		case "/v1/apps/app-123/deployments":
			decodeRequest(t, r, &deployed)
			writeJSON(t, w, http.StatusCreated, nanoflare.Deployment{ID: "deployment-456"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	writeProjectFile(t, Project{
		Name:              "Hello",
		Hostname:          "hello.example.com",
		APIURL:            server.URL,
		Entrypoint:        "worker.js",
		CompatibilityDate: "2025-12-10",
		Triggers:          nanoflare.TriggerConfig{Crons: []string{"*/5 * * * *"}},
		Vars: map[string]json.RawMessage{
			"API_HOST": json.RawMessage(`"example.com"`),
		},
		Files: []string{"worker.js"},
		Auth: ProjectAuth{
			ProtectedRoutes: []string{"/admin/*"},
		},
		Assets: ProjectAssets{
			Directory:        "public",
			Binding:          "STATIC",
			NotFoundHandling: "404-page",
		},
	})
	if err := os.WriteFile("worker.js", []byte("addEventListener('fetch', () => {});"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("public", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("public", "logo.svg"), []byte("<svg />"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, ".", "init")
	runGit(t, ".", "config", "user.email", "test@example.com")
	runGit(t, ".", "config", "user.name", "Nanoflare Test")
	runGit(t, ".", "add", projectFilename, "worker.js", filepath.Join("public", "logo.svg"))
	runGit(t, ".", "commit", "-m", "Deploy hello worker")
	commitHash := strings.TrimSpace(runGit(t, ".", "rev-parse", "HEAD"))

	runner := NewRunner(io.Discard, io.Discard)
	if err := runner.Run([]string{"create", "worker"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run([]string{"deploy", "worker"}); err != nil {
		t.Fatal(err)
	}

	project := readProject(t, projectFilename)
	if project.AppID != "app-123" {
		t.Fatalf("app id = %q", project.AppID)
	}
	if project.Hostname != "hello.example.com" {
		t.Fatalf("hostname = %q", project.Hostname)
	}
	if created.Name != "Hello" || created.Hostname != "hello.example.com" {
		t.Fatalf("create payload = %#v", created)
	}
	if len(created.Auth.ProtectedRoutes) != 1 || created.Auth.ProtectedRoutes[0] != "/admin/*" {
		t.Fatalf("create auth = %#v", created.Auth)
	}
	if updated.Auth == nil || len(updated.Auth.ProtectedRoutes) != 1 || updated.Auth.ProtectedRoutes[0] != "/admin/*" {
		t.Fatalf("update auth = %#v", updated.Auth)
	}
	if deployed.Entrypoint != "worker.js" || deployed.CompatibilityDate != "2025-12-10" {
		t.Fatalf("deploy payload = %#v", deployed)
	}
	if deployed.CommitHash != commitHash || deployed.CommitMessage != "Deploy hello worker" {
		t.Fatalf("deploy git metadata = hash %q message %q, want hash %q message %q", deployed.CommitHash, deployed.CommitMessage, commitHash, "Deploy hello worker")
	}
	if len(deployed.Triggers.Crons) != 1 || deployed.Triggers.Crons[0] != "*/5 * * * *" {
		t.Fatalf("deploy triggers = %#v", deployed.Triggers)
	}
	if got := string(deployed.Vars["API_HOST"]); got != `"example.com"` {
		t.Fatalf("deploy vars = %#v", deployed.Vars)
	}
	if len(deployed.Files) != 1 || deployed.Files[0].Path != "worker.js" || deployed.Files[0].Content == "" {
		t.Fatalf("deploy files = %#v", deployed.Files)
	}
	if len(deployed.Assets) != 1 || deployed.Assets[0].Path != "logo.svg" {
		t.Fatalf("deploy assets = %#v", deployed.Assets)
	}
	if deployed.AssetConfig.Binding != "STATIC" || deployed.AssetConfig.NotFoundHandling != "404-page" {
		t.Fatalf("asset config = %#v", deployed.AssetConfig)
	}
}

func TestSecretListAndDelete(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/apps/app-123/secrets":
			writeJSON(t, w, http.StatusOK, []nanoflare.Secret{{Name: "DB_URL", UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/apps/app-123/secrets/DB_URL":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	writeProjectFile(t, Project{
		Name:              "Hello",
		Hostname:          "hello.example.com",
		AppID:             "app-123",
		APIURL:            server.URL,
		Entrypoint:        "worker.js",
		CompatibilityDate: "2025-12-10",
		Files:             []string{"worker.js"},
	})

	var stdout bytes.Buffer
	runner := NewRunner(&stdout, io.Discard)
	if err := runner.Run([]string{"secret", "list"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "DB_URL\t2026-01-02T03:04:05Z") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	stdout.Reset()
	if err := runner.Run([]string{"secret", "delete", "DB_URL"}); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "Deleted secret DB_URL\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestSecretPutUsesValueArgument(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	var payload nanoflare.PutSecretInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/apps/app-123/secrets/DB_URL":
			decodeRequest(t, r, &payload)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	writeProjectFile(t, Project{
		Name:              "Hello",
		Hostname:          "hello.example.com",
		AppID:             "app-123",
		APIURL:            server.URL,
		Entrypoint:        "worker.js",
		CompatibilityDate: "2025-12-10",
		Files:             []string{"worker.js"},
	})

	var stdout bytes.Buffer
	runner := NewRunner(&stdout, io.Discard)
	if err := runner.Run([]string{"secret", "put", "DB_URL", "postgres://secret"}); err != nil {
		t.Fatal(err)
	}
	if payload.Value != "postgres://secret" {
		t.Fatalf("payload = %#v", payload)
	}
	if got := stdout.String(); got != "Updated secret DB_URL\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestCreateDoesNotPersistGeneratedHostname(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	var created nanoflare.CreateAppInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/apps" {
			http.NotFound(w, r)
			return
		}
		decodeRequest(t, r, &created)
		writeJSON(t, w, http.StatusCreated, nanoflare.App{ID: "app-123", Name: created.Name, Hostname: "hello-a1b2c3d4.example.com"})
	}))
	defer server.Close()

	writeProjectFile(t, Project{
		Name:              "Hello",
		APIURL:            server.URL,
		Entrypoint:        "worker.js",
		CompatibilityDate: "2025-12-10",
		Files:             []string{"worker.js"},
	})

	if err := NewRunner(io.Discard, io.Discard).Run([]string{"create"}); err != nil {
		t.Fatal(err)
	}
	project := readProject(t, projectFilename)
	if created.Name != "Hello" || created.Hostname != "" {
		t.Fatalf("create payload = %#v", created)
	}
	if project.AppID != "app-123" || project.Hostname != "" {
		t.Fatalf("project = %#v", project)
	}
	content, err := os.ReadFile(projectFilename)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte(`"hostname"`)) {
		t.Fatalf("project file unexpectedly contains hostname:\n%s", content)
	}
}

func TestProjectAssetsRunWorkerFirstJSONShapes(t *testing.T) {
	for _, test := range []struct {
		name       string
		payload    string
		always     bool
		routeCount int
	}{
		{name: "true", payload: `{"name":"Hello","hostname":"hello.example.com","api_url":"http://127.0.0.1:8080","entrypoint":"worker.js","compatibility_date":"2025-12-10","files":["worker.js"],"assets":{"run_worker_first":true}}`, always: true},
		{name: "omitted", payload: `{"name":"Hello","hostname":"hello.example.com","api_url":"http://127.0.0.1:8080","entrypoint":"worker.js","compatibility_date":"2025-12-10","files":["worker.js"],"assets":{}}`},
		{name: "routes", payload: `{"name":"Hello","hostname":"hello.example.com","api_url":"http://127.0.0.1:8080","entrypoint":"worker.js","compatibility_date":"2025-12-10","files":["worker.js"],"assets":{"run_worker_first":["/api/*","!/api/docs/*"]}}`, routeCount: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var project Project
			if err := json.Unmarshal([]byte(test.payload), &project); err != nil {
				t.Fatal(err)
			}
			if project.Assets.RunWorkerFirst.Always() != test.always {
				t.Fatalf("always = %v, want %v", project.Assets.RunWorkerFirst.Always(), test.always)
			}
			if len(project.Assets.RunWorkerFirst.Routes()) != test.routeCount {
				t.Fatalf("routes = %#v, want %d routes", project.Assets.RunWorkerFirst.Routes(), test.routeCount)
			}
		})
	}
}

func TestHelpIncludesCommandUsage(t *testing.T) {
	var stderr bytes.Buffer
	runner := NewRunner(io.Discard, &stderr)

	if err := runner.Run([]string{"help"}); err != nil {
		t.Fatal(err)
	}

	usage := stderr.String()
	for _, want := range []string{
		"nanoflare init [flags] [directory]",
		"nanoflare create [worker] [flags]",
		"nanoflare list [worker] [flags]",
		"nanoflare delete [worker] [app-id] [flags]",
		"nanoflare deploy [worker] [flags]",
		"nanoflare auth login [flags]",
		"nanoflare auth orgs",
		"nanoflare auth use-org <org-id>",
		"nanoflare auth whoami",
		"nanoflare auth logout",
		"nanoflare secret put [flags] <name> <value>",
		"nanoflare secret list [flags]",
		"nanoflare secret delete [flags] <name>",
		"nanoflare kv namespace create [flags] <name>",
		"nanoflare kv namespace list [flags]",
		"nanoflare kv namespace delete [flags] <namespace-id>",
		"nanoflare object-storage bucket create [flags] <name>",
		"nanoflare object-storage bucket list [flags]",
		"nanoflare object-storage bucket delete [flags] <bucket-id>",
	} {
		if !strings.Contains(usage, want) {
			t.Fatalf("help output missing %q:\n%s", want, usage)
		}
	}
}

func TestAuthConfigPathDefaultsToUserConfigNanoflareAuthJSON(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv(authStorePathEnv, "")

	path, err := authConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(configDir, "nanoflare", authFilename)
	if path != want {
		t.Fatalf("auth config path = %q, want %q", path, want)
	}
}

func TestAuthCommandsUseAuthStoreOverride(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	authPath := filepath.Join(t.TempDir(), "custom-auth.json")
	t.Setenv(authStorePathEnv, authPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/login":
			writeJSON(t, w, http.StatusOK, nanoflare.AuthSession{
				Token:       "session-token",
				ActiveOrgID: "org-123",
				User:        nanoflare.User{ID: "user-123", Email: "user@example.com"},
				Organizations: []nanoflare.Organization{
					{ID: "org-123", Name: "Example Org"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	runner := NewRunner(&stdout, io.Discard)
	if err := runner.Run([]string{"auth", "login", "--api-url", server.URL, "--email", "user@example.com", "--password", "secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(authPath); err != nil {
		t.Fatalf("stat override auth config: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "Logged in as user@example.com") {
		t.Fatalf("stdout = %q", got)
	}

	stdout.Reset()
	if err := runner.Run([]string{"auth", "whoami"}); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "user@example.com\norg\torg-123\n" {
		t.Fatalf("stdout = %q", got)
	}

	stdout.Reset()
	if err := runner.Run([]string{"auth", "logout"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(authPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("override auth config exists after logout: %v", err)
	}
}

func TestAuthLoginWebUsesConsoleLoginFlow(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	authPath := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv(authStorePathEnv, authPath)

	var openedURL string
	originalOpenBrowser := openBrowserFunc
	openBrowserFunc = func(target string) error {
		openedURL = target
		go func() {
			loginURL, err := url.Parse(target)
			if err != nil {
				t.Errorf("parse opened URL: %v", err)
				return
			}
			nextURL, err := url.Parse(loginURL.Query().Get("next"))
			if err != nil {
				t.Errorf("parse next URL: %v", err)
				return
			}
			callbackURL, err := url.Parse(nextURL.Query().Get("callback_url"))
			if err != nil {
				t.Errorf("parse callback URL: %v", err)
				return
			}
			query := callbackURL.Query()
			query.Set("code", "cli-code")
			query.Set("state", nextURL.Query().Get("state"))
			callbackURL.RawQuery = query.Encode()
			response, err := http.Get(callbackURL.String())
			if err != nil {
				t.Errorf("call callback URL: %v", err)
				return
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Errorf("callback status = %d", response.StatusCode)
			}
		}()
		return nil
	}
	t.Cleanup(func() {
		openBrowserFunc = originalOpenBrowser
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/cli/session":
			var input map[string]string
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input["code"] != "cli-code" {
				t.Fatalf("cli code = %q", input["code"])
			}
			writeJSON(t, w, http.StatusOK, nanoflare.AuthSession{
				Token:       "session-token",
				ActiveOrgID: "org-123",
				User:        nanoflare.User{ID: "user-123", Email: "user@example.com"},
				Organizations: []nanoflare.Organization{
					{ID: "org-123", Name: "Example Org"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	runner := NewRunner(&stdout, &stderr)
	if err := runner.Run([]string{"auth", "login", "--api-url", server.URL, "--web"}); err != nil {
		t.Fatal(err)
	}
	loginURL, err := url.Parse(openedURL)
	if err != nil {
		t.Fatal(err)
	}
	if loginURL.Scheme+"://"+loginURL.Host != server.URL || loginURL.Path != "/login" {
		t.Fatalf("opened URL = %q", openedURL)
	}
	nextURL, err := url.Parse(loginURL.Query().Get("next"))
	if err != nil {
		t.Fatal(err)
	}
	if nextURL.Path != "/cli-login" || nextURL.Query().Get("callback_url") == "" || nextURL.Query().Get("state") == "" {
		t.Fatalf("next URL = %q", loginURL.Query().Get("next"))
	}
	if got := stdout.String(); !strings.Contains(got, "Logged in as user@example.com") || !strings.Contains(got, "Using organization org-123") {
		t.Fatalf("stdout = %q", got)
	}
	if got := stderr.String(); !strings.Contains(got, "Open this URL to continue web login:\n"+openedURL) || !strings.Contains(got, "Opened browser for web login.") || !strings.Contains(got, "Waiting for browser login to complete...") {
		t.Fatalf("stderr = %q", got)
	}
	if _, err := os.Stat(authPath); err != nil {
		t.Fatalf("stat auth config: %v", err)
	}
}

func TestAuthLoginPATUsesPersonalAccessTokenSession(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	authPath := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv(authStorePathEnv, authPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/pat/session":
			var input map[string]string
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input["token"] != "pat-token" {
				t.Fatalf("pat token = %q", input["token"])
			}
			writeJSON(t, w, http.StatusOK, nanoflare.AuthSession{
				Token:       "server-echo-is-ignored",
				ActiveOrgID: "org-123",
				User:        nanoflare.User{ID: "user-123", Email: "user@example.com"},
				Organizations: []nanoflare.Organization{
					{ID: "org-123", Name: "Example Org"},
					{ID: "org-456", Name: "Other Org"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	runner := NewRunner(&stdout, &stderr)
	runner.Stdin = strings.NewReader("pat-token\n")
	if err := runner.Run([]string{"auth", "login", "--api-url", server.URL, "--pat"}); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); !strings.Contains(got, "Logged in as user@example.com") || !strings.Contains(got, "Using organization org-123") {
		t.Fatalf("stdout = %q", got)
	}
	if got := stderr.String(); got != "Personal access token: " {
		t.Fatalf("stderr = %q", got)
	}
	auth, err := loadAuthConfig()
	if err != nil {
		t.Fatal(err)
	}
	if auth.Token != "pat-token" || auth.RefreshToken != "" || len(auth.Orgs) != 2 {
		t.Fatalf("auth = %#v", auth)
	}
}

func TestAuthLoginOIDCFlagIsRemoved(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := NewRunner(&stdout, &stderr)
	err := runner.Run([]string{"auth", "login", "--oidc"})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined: -oidc") {
		t.Fatalf("error = %v", err)
	}
}

func TestRequestRefreshesAuthTokenAndRetries(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	authPath := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv(authStorePathEnv, authPath)

	appRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/apps":
			appRequests++
			if appRequests == 1 {
				writeJSON(t, w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer refreshed-token" {
				t.Fatalf("authorization = %q", got)
			}
			if got := r.Header.Get("X-Nanoflare-Org-ID"); got != "org-123" {
				t.Fatalf("org header = %q", got)
			}
			writeJSON(t, w, http.StatusOK, []nanoflare.App{{ID: "app-123", Name: "Hello", Hostname: "hello.example.com"}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/refresh":
			var input map[string]string
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input["refresh_token"] != "refresh-token" {
				t.Fatalf("refresh token = %q", input["refresh_token"])
			}
			writeJSON(t, w, http.StatusOK, nanoflare.AuthSession{
				Token:        "refreshed-token",
				RefreshToken: "rotated-refresh-token",
				ActiveOrgID:  "org-123",
				User:         nanoflare.User{ID: "user-123", Email: "user@example.com"},
				Organizations: []nanoflare.Organization{
					{ID: "org-123", Name: "Example Org"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := writeAuthConfig(AuthConfig{
		APIURL:       server.URL,
		Token:        "expired-token",
		RefreshToken: "refresh-token",
		ActiveOrgID:  "org-123",
		User:         nanoflare.User{ID: "user-123", Email: "user@example.com"},
		Orgs:         []nanoflare.Organization{{ID: "org-123", Name: "Example Org"}},
	}); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	runner := NewRunner(&stdout, io.Discard)
	if err := runner.Run([]string{"list", "--api-url", server.URL}); err != nil {
		t.Fatal(err)
	}
	if appRequests != 2 {
		t.Fatalf("app requests = %d", appRequests)
	}
	if got := stdout.String(); got != "app-123\tHello\thello.example.com\n" {
		t.Fatalf("stdout = %q", got)
	}
	auth, err := loadAuthConfig()
	if err != nil {
		t.Fatal(err)
	}
	if auth.Token != "refreshed-token" || auth.RefreshToken != "rotated-refresh-token" {
		t.Fatalf("auth = %#v", auth)
	}
}

func TestListWorkers(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/apps" {
			http.NotFound(w, r)
			return
		}
		writeJSON(t, w, http.StatusOK, []nanoflare.App{
			{ID: "app-123", Name: "Hello", Hostname: "hello.example.com"},
			{ID: "app-456", Name: "World", Hostname: "world.example.com"},
		})
	}))
	defer server.Close()

	var stdout bytes.Buffer
	runner := NewRunner(&stdout, io.Discard)
	if err := runner.Run([]string{"list", "--api-url", server.URL}); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "app-123\tHello\thello.example.com\napp-456\tWorld\tworld.example.com\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestDeleteRegisteredWorkerClearsLocalAppID(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/apps/app-123" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	writeProjectFile(t, Project{
		Name:              "Hello",
		Hostname:          "hello.example.com",
		AppID:             "app-123",
		APIURL:            server.URL,
		Entrypoint:        "worker.js",
		CompatibilityDate: "2025-12-10",
		Files:             []string{"worker.js"},
	})

	var stdout bytes.Buffer
	runner := NewRunner(&stdout, io.Discard)
	if err := runner.Run([]string{"delete"}); err != nil {
		t.Fatal(err)
	}
	project := readProject(t, projectFilename)
	if project.AppID != "" {
		t.Fatalf("app id = %q, want cleared", project.AppID)
	}
	if got := stdout.String(); got != "Deleted worker app-123\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestDeleteWorkerByID(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/apps/app-789" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	runner := NewRunner(io.Discard, io.Discard)
	if err := runner.Run([]string{"delete", "--api-url", server.URL, "app-789"}); err != nil {
		t.Fatal(err)
	}
}

func TestDeployRequiresRegisteredWorker(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	writeProjectFile(t, Project{
		Name:              "Hello",
		Hostname:          "hello.example.com",
		APIURL:            defaultAPIURL,
		Entrypoint:        "worker.js",
		CompatibilityDate: "2025-12-10",
		Files:             []string{"worker.js"},
	})

	err := NewRunner(io.Discard, io.Discard).Run([]string{"deploy"})
	if err == nil || !strings.Contains(err.Error(), "nanoflare create") {
		t.Fatalf("error = %v", err)
	}
}

func TestCreateReportsNanoflareError(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusConflict, map[string]string{"error": "hostname already exists"})
	}))
	defer server.Close()
	writeProjectFile(t, Project{
		Name:              "Hello",
		Hostname:          "hello.example.com",
		APIURL:            server.URL,
		Entrypoint:        "worker.js",
		CompatibilityDate: "2025-12-10",
		Files:             []string{"worker.js"},
	})

	err := NewRunner(io.Discard, io.Discard).Run([]string{"create"})
	if err == nil || !strings.Contains(err.Error(), "hostname already exists") {
		t.Fatalf("error = %v", err)
	}
}

func TestKVNamespaceCommands(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	var created nanoflare.CreateKVNamespaceInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/kv/namespaces":
			decodeRequest(t, r, &created)
			writeJSON(t, w, http.StatusCreated, nanoflare.KVNamespace{ID: "kvns-123", Name: created.Name})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/kv/namespaces":
			writeJSON(t, w, http.StatusOK, []nanoflare.KVNamespace{{ID: "kvns-123", Name: "sessions"}})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/kv/namespaces/kvns-123":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	runner := NewRunner(&stdout, io.Discard)
	if err := runner.Run([]string{"kv", "namespace", "create", "--api-url", server.URL, "sessions"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run([]string{"kv", "namespace", "list", "--api-url", server.URL}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run([]string{"kv", "namespace", "delete", "--api-url", server.URL, "kvns-123"}); err != nil {
		t.Fatal(err)
	}
	if created.Name != "sessions" {
		t.Fatalf("create payload = %#v", created)
	}
	if got := stdout.String(); got != "Created KV namespace kvns-123\tsessions\nkvns-123\tsessions\nDeleted KV namespace kvns-123\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestObjectStorageBucketCommands(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	var created nanoflare.CreateObjectStorageBucketInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/object-storage-buckets":
			decodeRequest(t, r, &created)
			writeJSON(t, w, http.StatusCreated, nanoflare.ObjectStorageBucket{ID: "bucket-123", Name: created.Name})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/object-storage-buckets":
			writeJSON(t, w, http.StatusOK, []nanoflare.ObjectStorageBucket{{ID: "bucket-123", Name: "customer-files"}})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/object-storage-buckets/bucket-123":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	runner := NewRunner(&stdout, io.Discard)
	if err := runner.Run([]string{"object-storage", "bucket", "create", "--api-url", server.URL, "customer-files"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run([]string{"object-storage", "bucket", "list", "--api-url", server.URL}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run([]string{"object-storage", "bucket", "delete", "--api-url", server.URL, "bucket-123"}); err != nil {
		t.Fatal(err)
	}
	if created.Name != "customer-files" {
		t.Fatalf("create payload = %#v", created)
	}
	if got := stdout.String(); got != "Created object storage bucket bucket-123\tcustomer-files\nbucket-123\tcustomer-files\nDeleted object storage bucket bucket-123\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestLoadProjectAcceptsLegacyObjectStorageBucketShape(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	if err := os.WriteFile(projectFilename, []byte(`{
  "name": "Hello",
  "hostname": "hello.example.com",
  "api_url": "http://127.0.0.1:8080",
  "entrypoint": "worker.js",
  "compatibility_date": "2025-12-10",
  "files": ["worker.js"],
  "object_storage_buckets": [
    { "binding": "OBJECTS", "id": "bucket-123" }
  ]
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, project, err := loadProject()
	if err != nil {
		t.Fatal(err)
	}
	if len(project.ObjectStorageBuckets) != 1 {
		t.Fatalf("object storage buckets = %#v", project.ObjectStorageBuckets)
	}
	if project.ObjectStorageBuckets[0].Binding != "OBJECTS" || project.ObjectStorageBuckets[0].BucketID != "bucket-123" {
		t.Fatalf("legacy object storage bucket = %#v", project.ObjectStorageBuckets[0])
	}
}

func withWorkingDirectory(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatal(err)
		}
	})
}

func writeProjectFile(t *testing.T, project Project) {
	t.Helper()
	if err := writeProject(projectFilename, project, os.O_TRUNC); err != nil {
		t.Fatal(err)
	}
}

func readProject(t *testing.T, path string) Project {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var project Project
	if err := json.Unmarshal(content, &project); err != nil {
		t.Fatal(err)
	}
	return project
}

func decodeRequest(t *testing.T, request *http.Request, target any) {
	t.Helper()
	if err := json.NewDecoder(request.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func writeJSON(t *testing.T, writer http.ResponseWriter, status int, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Fatal(err)
	}
}
