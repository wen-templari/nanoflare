package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/clas/nanoflare/internal/deployupload"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestDeploymentRefreshReopensStream(t *testing.T) {
	t.Setenv(authStorePathEnv, filepath.Join(t.TempDir(), "auth.json"))
	path := filepath.Join(t.TempDir(), "worker.js")
	code := "export default {fetch(){return new Response('ok')}}"
	if err := os.WriteFile(path, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/refresh" {
			writeJSON(t, w, 200, nanoflare.AuthSession{Token: "fresh", RefreshToken: "refresh", ActiveOrgID: "org", Organizations: []nanoflare.Organization{{ID: "org"}}})
			return
		}
		attempts++
		if attempts == 1 {
			// Reject before reading the stream, as real authentication middleware does.
			writeJSON(t, w, 401, map[string]string{"error": "expired"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer fresh" {
			t.Error("missing refreshed token")
		}
		input, err := deployupload.Decode(r.Body, r.Header.Get("Content-Type"), nanoflare.DefaultDeploymentLimits())
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if len(input.Files) != 1 || input.Files[0].Content != code {
			t.Error("retry lost file bytes")
		}
		writeJSON(t, w, 201, nanoflare.Deployment{ID: "revision"})
	}))
	defer server.Close()
	if err := writeAuthConfig(AuthConfig{APIURL: server.URL, Token: "expired", RefreshToken: "refresh", ActiveOrgID: "org", Orgs: []nanoflare.Organization{{ID: "org"}}}); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(io.Discard, io.Discard)
	input := nanoflare.DeployInput{Files: []nanoflare.WorkerFile{{Path: "worker.js"}}}
	sources := []deploymentSource{{name: "file-0", contentType: "application/javascript", diskPath: path, size: int64(len(code))}}
	var result nanoflare.Deployment
	if err := runner.requestDeployment(server.URL+"/v1/workers/worker/deployments", input, sources, &result); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || result.ID != "revision" {
		t.Fatalf("attempts=%d result=%v", attempts, result)
	}
}

func TestDeploymentStreamReportsChangedAndMissingFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(nanoflare.DeployInput{Files: []nanoflare.WorkerFile{{Path: "worker.js"}}})
	for _, source := range []deploymentSource{
		{name: "file-0", contentType: "application/javascript", diskPath: path, size: 1},
		{name: "file-0", contentType: "application/javascript", diskPath: path + "-missing", size: 1},
	} {
		body, _ := streamDeployment(metadata, []deploymentSource{source})
		_, err := io.Copy(io.Discard, body)
		body.Close()
		if err == nil {
			t.Fatal("upload silently accepted changed or missing file")
		}
	}
}

func TestDeploymentPreparationDoesNotLoadAssetContents(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	if err := os.Mkdir("public", 0700); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte{255, 0, 128}, 1024)
	if err := os.WriteFile("public/binary", data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("worker.js", []byte("export default {}"), 0600); err != nil {
		t.Fatal(err)
	}
	files, assets, sources, err := prepareDeploymentFiles(Project{Files: []string{"worker.js"}, Assets: ProjectAssets{Directory: "public"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Content != "" || len(assets) != 1 || assets[0].Data != nil || len(sources) != 2 || sources[1].size != int64(len(data)) {
		t.Fatal("preparation loaded contents or lost metadata")
	}
	body, kind := streamDeployment([]byte(`{"files":[{"path":"worker.js"}],"assets":[{"path":"binary"}]}`), sources)
	defer body.Close()
	decoded, err := deployupload.Decode(body, kind, nanoflare.DefaultDeploymentLimits())
	if err != nil || !bytes.Equal(decoded.Assets[0].Data, data) {
		t.Fatal("binary bytes changed", err)
	}
}
