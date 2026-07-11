package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

func TestWorkerdNativeKVBindingEndToEnd(t *testing.T) {
	workerd, err := exec.LookPath("workerd")
	if err != nil {
		t.Skip("workerd is not installed")
	}
	store := nanoflare.NewStore()
	app := nanoflare.App{ID: "native-kv", Name: "Native KV", Hostname: "native.example.com", RuntimeToken: "runtime-secret", CreatedAt: time.Now().UTC()}
	if err := store.CreateApp(app); err != nil {
		t.Fatal(err)
	}
	service := nanoflare.NewService(store, discardWriter{})
	namespace, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{Name: "native-kv"})
	if err != nil {
		t.Fatal(err)
	}
	runtimeServer := httptest.NewServer(api.NewRuntimeKVServer(service))
	defer runtimeServer.Close()

	port := availablePort(t)
	active := []nanoflare.ActiveDeployment{{
		App: app,
		Deployment: nanoflare.Deployment{
			ID:                "deployment",
			AppID:             app.ID,
			Files:             []nanoflare.WorkerFile{{Path: "worker.js", Content: nativeKVWorker}},
			Entrypoint:        "worker.js",
			Format:            "modules",
			CompatibilityDate: "2025-12-10",
			KVNamespaces:      []nanoflare.KVBinding{{Binding: "KV", ID: namespace.ID}},
			Port:              port,
			CreatedAt:         time.Now().UTC(),
		},
	}}
	configPath := filepath.Join(t.TempDir(), "workerd.capnp")
	if err := os.WriteFile(configPath, []byte(config.WorkerdWithRuntimeAddr(active, strings.TrimPrefix(runtimeServer.URL, "http://"))), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(ctx, workerd, "serve", configPath)
	output, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = command.Wait()
	}()
	errorOutput := make(chan string, 1)
	go func() {
		value, _ := io.ReadAll(output)
		errorOutput <- string(value)
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("worker status = %d, body = %s", response.StatusCode, body)
			}
			if got, want := string(body), `{"text":"hello","json":{"ok":true},"missing":null}`; got != want {
				t.Fatalf("worker body = %s, want %s", got, want)
			}
			return
		}
		select {
		case value := <-errorOutput:
			t.Fatalf("workerd exited before becoming ready: %s", value)
		case <-time.After(25 * time.Millisecond):
		}
	}
	t.Fatal("workerd did not become ready")
}

func TestLazyRuntimeGatewayEndToEnd(t *testing.T) {
	workerd, err := exec.LookPath("workerd")
	if err != nil {
		t.Skip("workerd is not installed")
	}
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, discardWriter{})
	app, err := service.CreateApp(nanoflare.CreateAppInput{Name: "Lazy", Hostname: "lazy.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deploy(app.ID, nanoflare.DeployInput{
		Files:             []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() { return new Response("lazy"); } };`}},
		Entrypoint:        "worker.js",
		Format:            "modules",
		CompatibilityDate: "2025-12-10",
	}); err != nil {
		t.Fatal(err)
	}
	runtimeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/runtime/durations" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer runtimeServer.Close()
	dir := t.TempDir()
	writer := config.NewRuntimeWriter(filepath.Join(dir, "workerd.capnp"), discardTraefik{})
	writer.SetNanoflareRuntimeAddr(strings.TrimPrefix(runtimeServer.URL, "http://"))
	manager := runtime.NewLazyManager(
		writer,
		runtime.CommandLauncher{Executable: workerd},
		dir,
		"127.0.0.1",
		availablePort(t),
		5*time.Second,
		5*time.Second,
		50*time.Millisecond,
	)
	defer manager.Close()
	server := api.NewServerWithRuntime(service, nil, "", nil, nil, manager)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/internal/http/apps/"+app.ID+"/", nil)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "lazy" {
		t.Fatalf("lazy gateway status = %d body = %q", recorder.Code, recorder.Body.String())
	}
}

func TestWorkerdAssetsBindingEndToEnd(t *testing.T) {
	workerd, err := exec.LookPath("workerd")
	if err != nil {
		t.Skip("workerd is not installed")
	}
	store := nanoflare.NewStore()
	objects := newE2EObjectStore()
	app := nanoflare.App{ID: "native-assets", Name: "Native Assets", Hostname: "assets.example.com", RuntimeToken: "runtime-secret", CreatedAt: time.Now().UTC()}
	if err := store.CreateApp(app); err != nil {
		t.Fatal(err)
	}
	service := nanoflare.NewServiceWithObjects(store, discardWriter{}, objects)
	deployment, err := service.Deploy(app.ID, nanoflare.DeployInput{
		Files:             []nanoflare.WorkerFile{{Path: "worker.js", Content: nativeAssetsWorker}},
		Assets:            []nanoflare.AssetFile{{Path: "logo.svg", ContentType: "image/svg+xml", Data: []byte("<svg />")}, {Path: "index.html", ContentType: "text/html; charset=utf-8", Data: []byte("<h1>Index</h1>")}},
		Entrypoint:        "worker.js",
		Format:            "modules",
		CompatibilityDate: "2025-12-10",
		AssetConfig:       nanoflare.AssetConfig{Binding: "ASSETS"},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeKV := api.NewRuntimeKVServer(service)
	runtimeAssets := api.NewRuntimeAssetServer(service)
	runtimeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Nanoflare-Binding") == "assets" {
			runtimeAssets.ServeHTTP(w, r)
			return
		}
		runtimeKV.ServeHTTP(w, r)
	}))
	defer runtimeServer.Close()

	port := availablePort(t)
	active := []nanoflare.ActiveDeployment{{
		App:        app,
		Deployment: deployment,
	}}
	active[0].Deployment.Files = []nanoflare.WorkerFile{{Path: "worker.js", Content: nativeAssetsWorker}}
	active[0].Deployment.Port = port
	configPath := filepath.Join(t.TempDir(), "workerd.capnp")
	if err := os.WriteFile(configPath, []byte(config.WorkerdWithRuntimeAddr(active, strings.TrimPrefix(runtimeServer.URL, "http://"))), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(ctx, workerd, "serve", configPath)
	output, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = command.Wait()
	}()
	errorOutput := make(chan string, 1)
	go func() {
		value, _ := io.ReadAll(output)
		errorOutput <- string(value)
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("worker status = %d, body = %s", response.StatusCode, body)
			}
			if got, want := string(body), "<svg />|<h1>Index</h1>"; got != want {
				t.Fatalf("worker body = %s, want %s", got, want)
			}
			return
		}
		select {
		case value := <-errorOutput:
			t.Fatalf("workerd exited before becoming ready: %s", value)
		case <-time.After(25 * time.Millisecond):
		}
	}
	t.Fatal("workerd did not become ready")
}

func TestWorkerdObjectsBindingEndToEnd(t *testing.T) {
	workerd, err := exec.LookPath("workerd")
	if err != nil {
		t.Skip("workerd is not installed")
	}
	store := nanoflare.NewStore()
	objects := newE2EObjectStore()
	app := nanoflare.App{ID: "native-objects", Name: "Native Objects", Hostname: "objects.example.com", RuntimeToken: "runtime-secret", CreatedAt: time.Now().UTC()}
	if err := store.CreateApp(app); err != nil {
		t.Fatal(err)
	}
	service := nanoflare.NewServiceWithObjects(store, discardWriter{}, objects)
	bucket, err := service.CreateObjectStorageBucket(nanoflare.CreateObjectStorageBucketInput{Name: "objects"})
	if err != nil {
		t.Fatal(err)
	}
	runtimeServer := httptest.NewServer(api.NewServer(service))
	defer runtimeServer.Close()

	port := availablePort(t)
	deployment, err := service.Deploy(app.ID, nanoflare.DeployInput{
		Files:                []nanoflare.WorkerFile{{Path: "worker.js", Content: nativeObjectsWorker}},
		Entrypoint:           "worker.js",
		Format:               "modules",
		CompatibilityDate:    "2025-12-10",
		ObjectStorageBuckets: []nanoflare.ObjectStorageBucketBinding{{Binding: "OBJECTS", BucketID: bucket.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	active := []nanoflare.ActiveDeployment{{App: app, Deployment: deployment}}
	active[0].Deployment.Port = port
	configPath := filepath.Join(t.TempDir(), "workerd.capnp")
	if err := os.WriteFile(configPath, []byte(config.WorkerdWithRuntimeAddr(active, strings.TrimPrefix(runtimeServer.URL, "http://"))), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(ctx, workerd, "serve", configPath)
	output, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = command.Wait()
	}()
	errorOutput := make(chan string, 1)
	go func() {
		value, _ := io.ReadAll(output)
		errorOutput <- string(value)
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("worker status = %d, body = %s", response.StatusCode, body)
			}
			if got, want := string(body), `{"created":{"key":"folder/demo.json","size":11,"etag":"etag-folder/demo.json","httpEtag":"\"etag-folder/demo.json\"","contentType":"application/json"},"head":{"key":"folder/demo.json","size":11},"body":{"ok":true},"missing":true}`; got != want {
				t.Fatalf("worker body = %s, want %s", got, want)
			}
			return
		}
		select {
		case value := <-errorOutput:
			t.Fatalf("workerd exited before becoming ready: %s", value)
		case <-time.After(25 * time.Millisecond):
		}
	}
	t.Fatal("workerd did not become ready")
}

func TestWorkerdDurationTelemetryEndToEnd(t *testing.T) {
	workerd, err := exec.LookPath("workerd")
	if err != nil {
		t.Skip("workerd is not installed")
	}

	var mu sync.Mutex
	var payloads []string
	runtimeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		payloads = append(payloads, string(body))
		mu.Unlock()
		t.Logf("runtime duration request: method=%s path=%s body=%s", r.Method, r.URL.Path, body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer runtimeServer.Close()

	port := availablePort(t)
	app := nanoflare.App{ID: "native-duration", Name: "Native Duration", Hostname: "duration.example.com", RuntimeToken: "runtime-secret", CreatedAt: time.Now().UTC()}
	active := []nanoflare.ActiveDeployment{{
		App: app,
		Deployment: nanoflare.Deployment{
			ID:                "deployment",
			AppID:             app.ID,
			Files:             []nanoflare.WorkerFile{{Path: "worker.js", Content: nativeDurationWorker}},
			Entrypoint:        "worker.js",
			Format:            "modules",
			CompatibilityDate: "2025-12-10",
			Port:              port,
			CreatedAt:         time.Now().UTC(),
		},
	}}
	generatedConfig := config.WorkerdWithRuntimeAddr(active, strings.TrimPrefix(runtimeServer.URL, "http://"))
	t.Logf("generated workerd config:\n%s", generatedConfig)
	configPath := filepath.Join(t.TempDir(), "workerd.capnp")
	if err := os.WriteFile(configPath, []byte(generatedConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(ctx, workerd, "serve", configPath)
	output, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = command.Wait()
	}()
	errorOutput := make(chan string, 1)
	go func() {
		value, _ := io.ReadAll(output)
		errorOutput <- string(value)
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("worker status = %d, body = %s", response.StatusCode, body)
			}
			if got, want := string(body), "done"; got != want {
				t.Fatalf("worker body = %s, want %s", got, want)
			}
			break
		}
		select {
		case value := <-errorOutput:
			t.Fatalf("workerd exited before becoming ready: %s", value)
		case <-time.After(25 * time.Millisecond):
		}
	}

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		current := append([]string(nil), payloads...)
		mu.Unlock()
		if len(current) > 0 {
			var events []struct {
				ScriptName string  `json:"scriptName"`
				DurationMs float64 `json:"durationMs"`
			}
			if err := json.Unmarshal([]byte(current[0]), &events); err != nil {
				t.Fatalf("duration payload is not json: %s: %v", current[0], err)
			}
			if len(events) != 1 || events[0].ScriptName != app.ID || events[0].DurationMs <= 0 {
				t.Fatalf("duration payload = %s, want one positive sample for %s", current[0], app.ID)
			}
			return
		}
		select {
		case value := <-errorOutput:
			t.Fatalf("workerd exited before posting duration telemetry: %s", value)
		case <-time.After(50 * time.Millisecond):
		}
	}
	cancel()
	stderr := <-errorOutput
	t.Fatalf("no duration telemetry posted; workerd stderr:\n%s", stderr)
}

func availablePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

type discardWriter struct{}

func (discardWriter) Write([]nanoflare.ActiveDeployment) error {
	return nil
}

type discardTraefik struct{}

func (discardTraefik) WriteTraefik([]nanoflare.ActiveDeployment) error {
	return nil
}

type e2eObjectStore struct {
	objects map[string]nanoflare.ObjectBody
}

func newE2EObjectStore() *e2eObjectStore {
	return &e2eObjectStore{objects: make(map[string]nanoflare.ObjectBody)}
}

func (s *e2eObjectStore) PresignUpload(string, string, time.Duration) (string, error) {
	return "", nil
}

func (s *e2eObjectStore) PresignDownload(string, string, time.Duration) (string, error) {
	return "", nil
}

func (s *e2eObjectStore) Put(appID, path string, contentType string, data []byte) (nanoflare.ObjectInfo, error) {
	visiblePath := path
	if strings.HasPrefix(path, "buckets/") {
		parts := strings.SplitN(path, "/", 3)
		if len(parts) == 3 {
			visiblePath = parts[2]
		}
	}
	object := nanoflare.ObjectBody{
		ObjectInfo: nanoflare.ObjectInfo{
			Key:      path,
			Size:     int64(len(data)),
			ETag:     "etag-" + visiblePath,
			HTTPETag: `"etag-` + visiblePath + `"`,
			Uploaded: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			HTTPMetadata: nanoflare.ObjectHTTPMetadata{
				ContentType: contentType,
			},
		},
		Body: append([]byte(nil), data...),
	}
	s.objects[appID+"/"+path] = object
	return object.ObjectInfo, nil
}

func (s *e2eObjectStore) Get(appID, path string) (nanoflare.ObjectBody, error) {
	object, ok := s.objects[appID+"/"+path]
	if !ok {
		return nanoflare.ObjectBody{}, nanoflare.ErrObjectNotFound
	}
	object.Body = append([]byte(nil), object.Body...)
	return object, nil
}

func (s *e2eObjectStore) Head(appID, path string) (nanoflare.ObjectInfo, error) {
	object, ok := s.objects[appID+"/"+path]
	if !ok {
		return nanoflare.ObjectInfo{}, nanoflare.ErrObjectNotFound
	}
	return object.ObjectInfo, nil
}

func (s *e2eObjectStore) List(appID, prefix string) ([]nanoflare.ObjectInfo, error) {
	items := make([]nanoflare.ObjectInfo, 0)
	for key, data := range s.objects {
		if !strings.HasPrefix(key, appID+"/"+prefix+"/") {
			continue
		}
		object := data.ObjectInfo
		object.Key = strings.TrimPrefix(object.Key, prefix+"/")
		items = append(items, object)
	}
	return items, nil
}

func (s *e2eObjectStore) Delete(appID, path string) error {
	delete(s.objects, appID+"/"+path)
	return nil
}

const nativeKVWorker = `export default {
  async fetch(request, env) {
    await env.KV.put("text", "hello");
    await env.KV.put("json", JSON.stringify({ ok: true }));
    const text = await env.KV.get("text");
    const json = await env.KV.get("json", "json");
    await env.KV.delete("text");
    const missing = await env.KV.get("text");
    return Response.json({ text, json, missing });
  },
};`

const nativeAssetsWorker = `export default {
  async fetch(_request, env) {
    const direct = await env.ASSETS.fetch("/logo.svg");
    const forwarded = await env.ASSETS.fetch(new Request("https://assets.local/index.html?x=1"));
    return new Response(await direct.text() + "|" + await forwarded.text());
  },
};`

const nativeObjectsWorker = `export default {
  async fetch(_request, env) {
    const created = await env.OBJECTS.put("folder/demo.json", JSON.stringify({ ok: true }), {
      httpMetadata: { contentType: "application/json" },
    });
    const body = await env.OBJECTS.get("folder/demo.json");
    const head = await env.OBJECTS.head("folder/demo.json");
    await env.OBJECTS.delete("folder/demo.json");
    const missing = await env.OBJECTS.get("folder/demo.json");
    return Response.json({
      created: {
        key: created.key,
        size: created.size,
        etag: created.etag,
        httpEtag: created.httpEtag,
        contentType: created.httpMetadata.contentType,
      },
      head: head ? { key: head.key, size: head.size } : null,
      body: body ? await body.json() : null,
      missing: missing === null,
    });
	  },
	};`

const nativeDurationWorker = `export default {
  fetch() {
    let value = 0;
    for (let index = 0; index < 5000000; index++) {
      value += Math.sqrt(index);
    }
    return new Response("done");
  },
};`
