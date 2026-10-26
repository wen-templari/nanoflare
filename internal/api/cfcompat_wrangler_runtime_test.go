package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestWranglerRuntimeWorkflow(t *testing.T) {
	wranglerRuntimeWorkflow(t)
}

func TestWranglerRuntimePostgresWorkflow(t *testing.T) {
	if os.Getenv("NANOFLARE_TEST_POSTGRES_URL") == "" {
		t.Skip("disposable PostgreSQL required")
	}
	t.Setenv("NANOFLARE_TEST_RUNTIME_BACKEND", "postgres")
	wranglerRuntimeWorkflow(t)
}

func wranglerRuntimeWorkflow(t *testing.T) {
	cli := os.Getenv("NANOFLARE_TEST_WRANGLER_PATH")
	if cli == "" || os.Getenv("NANOFLARE_TEST_WORKERD") == "" {
		t.Skip("set pinned Wrangler and native workerd executable paths")
	}
	if !filepath.IsAbs(cli) {
		t.Fatal("Wrangler path must be absolute")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(cli)), "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &pkg) != nil || pkg.Version != "4.54.0" {
		t.Fatal("expected Wrangler 4.54.0")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	guard, err := filepath.Abs("../../scripts/wfp-client-flows/wrangler-loopback.cjs")
	if err != nil {
		t.Fatal(err)
	}
	privateDynamicDispatchWithFixture(t, false, true, func(handler *Server, service *nanoflare.Service, org nanoflare.Organization, token string) {
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		handler.SetCloudflareCompatibilityEnabled(true)
		server := httptest.NewServer(handler)
		defer server.Close()
		directory := t.TempDir()
		must(os.Mkdir(filepath.Join(directory, "src"), 0700))
		write := func(name, content string) {
			t.Helper()
			must(os.WriteFile(filepath.Join(directory, name), []byte(content), 0600))
		}
		config := func(value map[string]any) {
			t.Helper()
			value["account_id"], value["compatibility_date"] = org.ID, "2025-12-10"
			value["workers_dev"], value["send_metrics"], value["no_bundle"] = false, false, true
			body, err := json.Marshal(value)
			must(err)
			write("wrangler.json", string(body))
		}
		run := func(stdin string, args ...string) {
			t.Helper()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, node, append([]string{"--require", guard, cli}, args...)...)
			command.WaitDelay = 2 * time.Second
			command.Dir, command.Stdin = directory, strings.NewReader(stdin)
			command.Env = []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + directory, "XDG_CONFIG_HOME=" + directory, "NF_WRANGLER_FIXTURE_DIRECTORY=" + directory, "CLOUDFLARE_API_TOKEN=" + token, "CLOUDFLARE_ACCOUNT_ID=" + org.ID, "CLOUDFLARE_API_BASE_URL=" + server.URL + "/client/v4", "WRANGLER_SEND_METRICS=false", "CI=true", "NO_COLOR=1", "WRANGLER_LOG_PATH=" + filepath.Join(directory, "wrangler.log")}
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("Wrangler %v: %v\n%s", args, err, output)
			}
		}
		putNamespaceSecret := func(value string) {
			t.Helper()
			body, err := json.Marshal(map[string]string{"name": "TOKEN", "type": "secret_text", "text": value})
			must(err)
			request := httptest.NewRequest("PUT", "/client/v4/accounts/"+org.ID+"/workers/dispatch/namespaces/wrangler-runtime/scripts/customer/secrets", strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != 200 {
				t.Fatalf("namespace secret: %d %s", response.Code, response.Body.String())
			}
		}
		config(map[string]any{"name": "customer", "main": "src/customer.js"})
		write("src/customer.js", `export default {fetch(request,env){return Response.json({version:"v1",secret:env.TOKEN})}}`)
		run("", "dispatch-namespace", "create", "wrangler-runtime")
		run("", "deploy", "--dispatch-namespace", "wrangler-runtime", "--keep-vars")
		putNamespaceSecret("wrangler-secret")
		write("src/dispatcher.js", `export default {async fetch(request,env){const response=await env.CUSTOMERS.get("customer").fetch(request);const headers=new Headers(response.headers);headers.set("X-Dispatcher-Secret",env.ROUTE_TOKEN||"");return new Response(response.body,{status:response.status,headers})}}`)
		config(map[string]any{"name": "wrangler-dispatcher", "main": "src/dispatcher.js", "dispatch_namespaces": []map[string]string{{"binding": "CUSTOMERS", "namespace": "wrangler-runtime"}}})
		run("", "deploy")
		run("dispatcher-secret\n", "secret", "put", "ROUTE_TOKEN")
		run("", "secret", "list")
		dispatcherSecret := "dispatcher-secret"
		dispatcher, err := service.OrdinaryScriptForOrg(org.ID, "wrangler-dispatcher")
		must(err)
		invoke := func(path string) string {
			t.Helper()
			response, err := server.Client().Get(server.URL + "/internal/http/workers/" + dispatcher.ID + path)
			must(err)
			defer response.Body.Close()
			if response.Header.Get("X-Dispatcher-Secret") != dispatcherSecret {
				t.Fatalf("dispatcher secret mismatch")
			}
			body, err := io.ReadAll(response.Body)
			must(err)
			if response.StatusCode != 200 {
				t.Fatalf("dispatch %d: %s", response.StatusCode, body)
			}
			return string(body)
		}
		check := func(version, secret string) {
			t.Helper()
			var result map[string]string
			must(json.Unmarshal([]byte(invoke("/api/check")), &result))
			if result["version"] != version || result["secret"] != secret {
				t.Fatalf("runtime response: %v", result)
			}
		}
		check("v1", "wrangler-secret")
		must(os.Mkdir(filepath.Join(directory, "public"), 0700))
		write("public/asset.txt", "Wrangler runtime asset")
		write("src/customer.js", `export default {fetch(request,env){return Response.json({version:"v2",secret:env.TOKEN})}}`)
		config(map[string]any{"name": "customer", "main": "src/customer.js", "assets": map[string]any{"directory": "./public", "run_worker_first": []string{"/api/*"}}})
		run("", "deploy", "--dispatch-namespace", "wrangler-runtime", "--keep-vars")
		check("v2", "wrangler-secret")
		if got := invoke("/asset.txt"); got != "Wrangler runtime asset" {
			t.Fatalf("asset: %q", got)
		}
		putNamespaceSecret("updated-secret")
		check("v2", "updated-secret")
		if got := invoke("/asset.txt"); got != "Wrangler runtime asset" {
			t.Fatalf("retained asset: %q", got)
		}
		run("dispatcher-updated\n", "secret", "put", "ROUTE_TOKEN", "--name", "wrangler-dispatcher")
		dispatcherSecret = "dispatcher-updated"
		check("v2", "updated-secret")
		run("y\n", "secret", "delete", "ROUTE_TOKEN", "--name", "wrangler-dispatcher")
		dispatcherSecret = ""
		check("v2", "updated-secret")
		wasmCode := `import module from "./answer.wasm";const instance=new WebAssembly.Instance(module);export default {fetch(request,env){return Response.json({version:"wasm",secret:env.TOKEN,answer:String(instance.exports.run())})}}`
		write("src/customer.js", wasmCode)
		wasm := func(answer byte) []byte {
			data, err := hex.DecodeString(fmt.Sprintf("0061736d010000000105016000017f030201000707010372756e00000a0601040041%02x0b00040178fffe", answer))
			must(err)
			return data
		}
		downloadBinary := func(want []byte) {
			t.Helper()
			request := httptest.NewRequest("GET", "/client/v4/accounts/"+org.ID+"/workers/dispatch/namespaces/wrangler-runtime/scripts/customer/content", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != 200 {
				t.Fatalf("binary content download: %d %s", response.Code, response.Body.String())
			}
			_, params, err := mime.ParseMediaType(response.Header().Get("Content-Type"))
			must(err)
			reader := multipart.NewReader(response.Body, params["boundary"])
			found := false
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				must(err)
				data, err := io.ReadAll(part)
				must(err)
				if filepath.Base(part.FileName()) == "answer.wasm" {
					found = true
					if part.Header.Get("Content-Type") != "application/wasm" || !bytes.Equal(data, want) {
						t.Fatal("Wrangler binary content corrupted")
					}
				}
			}
			if !found {
				t.Fatal("Wrangler binary content missing")
			}
		}
		checkWasm := func(answer, secret string) {
			t.Helper()
			var result map[string]string
			must(json.Unmarshal([]byte(invoke("/api/check")), &result))
			if result["version"] != "wasm" || result["secret"] != secret || result["answer"] != answer {
				t.Fatalf("Wasm runtime response: %v", result)
			}
			if got := invoke("/asset.txt"); got != "Wrangler runtime asset" {
				t.Fatalf("Wasm asset retention: %q", got)
			}
		}
		config(map[string]any{"name": "customer", "main": "src/customer.js", "find_additional_modules": true, "assets": map[string]any{"directory": "./public", "run_worker_first": []string{"/api/*"}}})
		for _, answer := range []byte{42, 7} {
			time.Sleep(1600 * time.Millisecond)
			binary := wasm(answer)
			must(os.WriteFile(filepath.Join(directory, "src/answer.wasm"), binary, 0600))
			run("", "deploy", "--dispatch-namespace", "wrangler-runtime", "--keep-vars")
			checkWasm(fmt.Sprint(answer), "updated-secret")
			downloadBinary(binary)
		}
		time.Sleep(1600 * time.Millisecond)
		putNamespaceSecret("wasm-secret")
		checkWasm("7", "wasm-secret")
		downloadBinary(wasm(7))
		namespaces, err := service.DispatchNamespacesForOrg(org.ID)
		must(err)
		if len(namespaces) != 1 {
			t.Fatal("namespace scope changed")
		}
		customer, err := service.NamespaceScript(org.ID, namespaces[0].ID, "customer")
		must(err)
		must(service.DeleteAppForOrg(org.ID, customer.ID))
		run("", "dispatch-namespace", "delete", "wrangler-runtime")
		run("", "delete", "--name", "wrangler-dispatcher", "--force")
		if _, err := service.OrdinaryScriptForOrg(org.ID, "wrangler-dispatcher"); err != nanoflare.ErrAppNotFound {
			t.Fatalf("ordinary CLI cleanup failed: %v", err)
		}
		t.Log("Wrangler 4.54.0 runtime dispatch, Wasm upload/execution/binary update, compatible-API binary download, code/secret updates, assets and cleanup passed")
	})
}
