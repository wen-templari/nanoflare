package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/cli"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestCLIDeployLargeFrontendWithRawAssets(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	repo := nanoflare.NewStore()
	auth := nanoflare.NewControlAuthService(repo, "multipart-cli")
	auth.SetTestHashCost(4)
	session, err := auth.Signup(nanoflare.SignupInput{Email: "upload@example.com", Password: "password", OrganizationName: "Uploads"})
	if err != nil {
		t.Fatal(err)
	}
	org, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "Uploads"})
	if err != nil {
		t.Fatal(err)
	}
	service := nanoflare.NewServiceWithObjects(repo, discardWriter{}, newAPIObjectStore())
	if err := service.SetBaseHostname("example.com"); err != nil {
		t.Fatal(err)
	}
	handler := NewServerWithRuntime(service, nil, "", nil, auth, nil)
	uploaded := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deployments") {
			uploaded = true
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data;") || r.ContentLength != -1 {
				t.Error("CLI did not stream multipart")
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	t.Setenv("NANOFLARE_TOKEN", session.Token)
	t.Setenv("NANOFLARE_ORG_ID", org.ID)
	t.Setenv("NANOFLARE_AUTH_STORE", filepath.Join(dir, "auth.json"))
	t.Setenv("NANOFLARED_URL", server.URL)
	code := "export default {fetch(){return new Response('frontend')}};/*" + strings.Repeat("x", 11<<20) + "*/"
	html := []byte("<!doctype html><h1>Uploaded</h1><!--" + strings.Repeat("x", 11<<20) + "-->")
	binary := []byte{0, 255, 128, 13, 10, 0, 255}
	if err = os.Mkdir("public", 0700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{"worker.js": []byte(code), "public/index.html": html, "public/blob.bin": binary, "nanoflare.json": []byte(`{"name":"frontend","hostname":"frontend.example.com","main":"worker.js","files":["worker.js"],"format":"modules","compatibility_date":"2025-12-10","assets":{"directory":"public"}}`)} {
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	runner := cli.NewRunner(&stdout, &stderr)
	if err = runner.Run([]string{"deploy", "--api-url", server.URL}); err != nil {
		t.Fatalf("deploy: %v (%s)", err, stderr.String())
	}
	if !uploaded {
		t.Fatal("deployment was not uploaded")
	}
	apps, err := repo.ListApps()
	if err != nil || len(apps) != 1 {
		t.Fatalf("apps: %v %v", apps, err)
	}
	files, err := service.WorkerFiles(apps[0].ID)
	if err != nil || len(files) != 1 || files[0].Content != code {
		t.Fatal("large worker code did not round-trip", err)
	}
	for path, want := range map[string][]byte{"/": html, "/blob.bin": binary} {
		response, handled, err := service.PublicAsset(apps[0].ID, path)
		if err != nil || !handled || response.StatusCode != 200 || !bytes.Equal(response.Body, want) {
			t.Fatalf("asset %s did not round-trip: handled=%v err=%v", path, handled, err)
		}
	}
}

func TestDeploymentUploadRejectsPartialAndOversizedWithoutActivation(t *testing.T) {
	service := nanoflare.NewService(nanoflare.NewStore(), discardWriter{})
	limits := nanoflare.DeploymentLimits{WorkerBytes: 32, AssetBytes: 64, RequestBytes: 4096}
	if err := service.SetDeploymentLimits(limits); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(service)
	app := createApp(t, server, "Multipart", "multipart.example.com")
	input := nanoflare.DeployInput{Entrypoint: "worker.js", Files: []nanoflare.WorkerFile{{Path: "worker.js"}}, CompatibilityDate: "2025-12-10"}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormField("metadata")
	json.NewEncoder(part).Encode(input)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file-0"; filename="worker.js"`)
	h.Set("Content-Type", "application/javascript")
	part, _ = writer.CreatePart(h)
	part.Write(bytes.Repeat([]byte{'x'}, 33))
	writer.Close()
	kind := writer.FormDataContentType()
	for _, tc := range []struct {
		name   string
		body   []byte
		kind   string
		status int
	}{
		{"code limit", body.Bytes(), kind, 413},
		{"interrupted", body.Bytes()[:bytes.Index(body.Bytes(), bytes.Repeat([]byte{'x'}, 33))+10], kind, 400},
		{"request limit", []byte(`{}` + strings.Repeat(" ", 4096)), "application/json", 413},
		{"malformed", []byte("{bad"), "application/json", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/v1/workers/"+app.ID+"/deployments", bytes.NewReader(tc.body))
			request.Header.Set("Content-Type", tc.kind)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			detail, err := service.WorkerDetail(app.ID)
			if err != nil || detail.Deployment != nil {
				t.Fatal("failed upload activated a deployment", err)
			}
		})
	}
}
