package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestWranglerLocalRequestQualification(t *testing.T) {
	cli := os.Getenv("NANOFLARE_TEST_WRANGLER_PATH")
	if cli == "" {
		t.Skip("set NANOFLARE_TEST_WRANGLER_PATH to wrangler@4.54.0/wrangler-dist/cli.js")
	}
	if !filepath.IsAbs(cli) {
		t.Fatal("Wrangler entrypoint must be absolute")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(cli)), "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &pkg) != nil || pkg.Version != "4.54.0" {
		t.Fatal("qualify this fixture before changing Wrangler versions")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	guard, err := filepath.Abs("../../scripts/wfp-client-flows/wrangler-loopback.cjs")
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			var repo nanoflare.Repository = nanoflare.NewStore()
			if backend == "postgres" {
				repo = runtimePostgres(t)
			}
			auth := nanoflare.NewControlAuthService(repo, "wrangler-fixture")
			auth.SetTestHashCost(4)
			session, err := auth.Signup(nanoflare.SignupInput{Email: "wrangler@example.com", Password: "password"})
			must(err)
			org, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "wrangler"})
			must(err)
			service, _ := managedService(t, repo, discardWriter{})
			handler := NewServerWithControlAuth(service, nil, "", nil, auth)
			handler.SetCloudflareCompatibilityEnabled(true)
			var mu sync.Mutex
			paths := []string{}
			metadataKeys := []string{}
			metadataType := ""
			wrap := func() http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					paths = append(paths, r.Method+" "+r.URL.Path)
					mu.Unlock()
					if kind, params, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && kind == "multipart/form-data" {
						body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
						if err != nil {
							t.Error(err)
							return
						}
						r.Body = io.NopCloser(bytes.NewReader(body))
						reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
						for {
							part, err := reader.NextPart()
							if err != nil {
								break
							}
							if part.FormName() == "metadata" {
								raw, _ := io.ReadAll(part)
								var object map[string]json.RawMessage
								_ = json.Unmarshal(raw, &object)
								keys := []string{}
								for key := range object {
									keys = append(keys, key)
								}
								sort.Strings(keys)
								mu.Lock()
								metadataKeys = keys
								metadataType = part.Header.Get("Content-Type")
								mu.Unlock()
							}
						}
					}
					handler.ServeHTTP(w, r)
				})
			}
			direct := httptest.NewServer(wrap())
			defer direct.Close()
			directory := t.TempDir()
			must(os.WriteFile(filepath.Join(directory, "worker.js"), []byte(`export default {fetch(){return new Response("wrangler-fixture")}}`), 0600))
			config, _ := json.Marshal(map[string]any{"name": "alpha", "main": "worker.js", "account_id": org.ID, "compatibility_date": "2025-12-10", "workers_dev": false, "preview_urls": false, "send_metrics": false, "no_bundle": true})
			must(os.WriteFile(filepath.Join(directory, "wrangler.json"), config, 0600))
			run := func(base string, wantSuccess bool, args ...string) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, node, append([]string{"--require", guard, cli}, args...)...)
				command.Dir = directory
				command.Stdin = strings.NewReader("fixture-secret\n")
				command.Env = []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + directory, "XDG_CONFIG_HOME=" + directory, "NF_WRANGLER_FIXTURE_DIRECTORY=" + directory, "CLOUDFLARE_API_TOKEN=" + session.Token, "CLOUDFLARE_ACCOUNT_ID=" + org.ID, "CLOUDFLARE_API_BASE_URL=" + base + "/client/v4", "WRANGLER_SEND_METRICS=false", "CI=true", "NO_COLOR=1", "WRANGLER_LOG_PATH=" + filepath.Join(directory, "wrangler.log")}
				out, err := command.CombinedOutput()
				if (err == nil) != wantSuccess {
					t.Fatalf("Wrangler %v: %v\n%s", args, err, out)
				}
				return string(out)
			}
			run(direct.URL, true, "dispatch-namespace", "list")
			run(direct.URL, true, "dispatch-namespace", "create", "staging")
			run(direct.URL, true, "dispatch-namespace", "get", "staging")
			run(direct.URL, true, "deploy", "--dispatch-namespace", "staging")
			ns, err := service.DispatchNamespacesForOrg(org.ID)
			must(err)
			if len(ns) != 1 {
				t.Fatal("Wrangler namespace creation changed scope")
			}
			app, err := service.NamespaceScript(org.ID, ns[0].ID, "alpha")
			must(err)
			upload, found, err := service.ManagementUploadForOrg(org.ID, app.ID, false)
			must(err)
			if !found || len(upload.Deployment.Files) != 1 || !strings.Contains(upload.Deployment.Files[0].Content, "wrangler-fixture") {
				t.Fatal("Wrangler did not publish its native snapshot")
			}
			first := upload.DeploymentID
			must(os.WriteFile(filepath.Join(directory, "worker.js"), []byte(`export default {fetch(){return new Response("wrangler-updated")}}`), 0600))
			run(direct.URL, true, "deploy", "--dispatch-namespace", "staging", "--var", "MESSAGE:updated")
			upload, found, err = service.ManagementUploadForOrg(org.ID, app.ID, false)
			must(err)
			if !found || upload.DeploymentID == first || !strings.Contains(upload.Deployment.Files[0].Content, "wrangler-updated") || string(upload.Deployment.Vars["MESSAGE"]) != `"updated"` {
				t.Fatal("Wrangler update did not preserve native code/variable pairing")
			}
			must(service.DeleteAppForOrg(org.ID, app.ID))
			run(direct.URL, true, "dispatch-namespace", "delete", "staging")
			remaining, err := service.DispatchNamespacesForOrg(org.ID)
			must(err)
			if len(remaining) != 0 {
				t.Fatal("Wrangler namespace deletion did not finish")
			}
			// Ordinary redeploys use Versions/Deployments rather than namespace PUT.
			must(service.SetBaseHostname("workers.example.test"))
			run(direct.URL, true, "deploy")
			ordinary, err := service.OrdinaryScriptForOrg(org.ID, "alpha")
			must(err)
			if ordinary.Hostname == "" {
				t.Fatal("ordinary Worker has no default hostname")
			}
			run(direct.URL, true, "secret", "put", "TOKEN")
			before, _, err := service.ManagementUploadForOrg(org.ID, ordinary.ID, false)
			must(err)
			must(os.WriteFile(filepath.Join(directory, "worker.js"), []byte(`export default {fetch(){return new Response("ordinary-updated")}}`), 0600))
			output := run(direct.URL, true, "deploy", "--keep-vars")
			if strings.Contains(output, "Could not apply service and environment tags") {
				t.Fatal("Wrangler post-deploy settings were rejected")
			}
			after, _, err := service.ManagementUploadForOrg(org.ID, ordinary.ID, false)
			must(err)
			if after.DeploymentID == before.DeploymentID || !strings.Contains(after.Deployment.Files[0].Content, "ordinary-updated") || !reflect.DeepEqual(after.SecretRefs, before.SecretRefs) {
				t.Fatal("ordinary redeploy lost code or secrets")
			}
			active, err := service.ActiveDeployments()
			must(err)
			if len(active) != 1 || active[0].Deployment.ID != after.DeploymentID || active[0].App.Hostname != ordinary.Hostname {
				t.Fatalf("ordinary redeploy traffic/hostname: %#v", active)
			}
			run(direct.URL, true, "d1", "list", "--json")
			run(direct.URL, true, "delete", "--force")
			mu.Lock()
			t.Logf("Wrangler %s local paths=%v upload metadata keys=%v metadata content type=%q", pkg.Version, paths, metadataKeys, metadataType)
			mu.Unlock()
		})
	}
}

func TestWranglerNetworkGuardRejectsExternalSockets(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	guard, err := filepath.Abs("../../scripts/wfp-client-flows/wrangler-loopback.cjs")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--require", guard, "--eval", `const assert=require('node:assert/strict');assert.throws(()=>require('node:net').connect({host:'api.cloudflare.com',port:443}),/blocked a non-loopback connection/);`)
	command.Env = []string{"NF_WRANGLER_FIXTURE_DIRECTORY=" + t.TempDir()}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("network guard: %v\n%s", err, output)
	}
}
