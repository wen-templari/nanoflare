package api_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

func TestWorkerdKitesurfNavigation(t *testing.T) {
	artifact := os.Getenv("NANOFLARE_TEST_KITESURF_ARTIFACT")
	if artifact == "" {
		t.Skip("set NANOFLARE_TEST_KITESURF_ARTIFACT to the extracted Kitesurf image's /opt/kitesurf directory")
	}
	workerd := os.Getenv("NANOFLARE_TEST_WORKERD")
	if workerd == "" {
		t.Skip("set NANOFLARE_TEST_WORKERD to the pinned native workerd")
	}
	dist := filepath.Join(artifact, "dist")
	store := nanoflare.NewStore()
	if err := store.CreateOrganization(nanoflare.Organization{ID: "kitesurf", Name: "Kitesurf"}); err != nil {
		t.Fatal(err)
	}
	publication := &dynamicPublicationWriter{}
	service := nanoflare.NewServiceWithObjects(store, publication, newE2EObjectStore())
	limits := nanoflare.DefaultDeploymentLimits()
	limits.WorkerBytes = 45 << 20
	limits.RequestBytes = 100 << 20
	if err := service.SetDeploymentLimits(limits); err != nil {
		t.Fatal(err)
	}
	service.SetWorkerLoadersEnabled(true)
	control := api.NewRuntimeExecutionServer(service, "kitesurf-test")
	defer control.Close()
	privateMux := http.NewServeMux()
	privateMux.Handle("/internal/runtime/executions/", control)
	privateMux.Handle("/internal/runtime/resources/", http.StripPrefix("/internal/runtime/resources", api.NewRuntimeAssetServer(service)))
	privateMux.Handle("/internal/runtime/durations", api.NewRuntimeDurationServer(service, runtime.NewDurationTelemetry()))
	private := httptest.NewServer(privateMux)
	defer private.Close()
	dir := t.TempDir()
	writer := config.NewRuntimeWriter(filepath.Join(dir, "workerd.capnp"), discardTraefik{})
	writer.SetNanoflareRuntimeAddr(strings.TrimPrefix(private.URL, "http://"))
	launcher := &dynamicPublicationLauncher{command: runtime.CommandLauncher{Executable: workerd, Experimental: true}}
	manager := runtime.NewDynamicManager(writer, launcher, dir, filepath.Join(dir, "workerd.capnp"), testSocketDir(t), 5*time.Second, time.Second)
	manager.SetRuntimeControlToken("kitesurf-test")
	publication.manager = manager
	defer manager.Close()
	app, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "kitesurf", Name: "kitesurf", Hostname: "kitesurf.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dist)
	if err != nil {
		t.Fatal(err)
	}
	var files []nanoflare.WorkerFile
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".map") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dist, name))
		if err != nil {
			t.Fatal(err)
		}
		file := nanoflare.WorkerFile{Path: name}
		switch {
		case strings.HasSuffix(name, ".wasm"):
			file.Wasm = content
		case name == "index.js":
			file.Content = string(content)
		default:
			encoded, _ := json.Marshal(string(content))
			file.Content = "export default " + string(encoded) + ";"
		}
		files = append(files, file)
	}
	// The bundled index exports its class by alias. Declare an explicit class
	// entrypoint so Nanoflare selects its class instrumentation for the bundle.
	files = append(files, nanoflare.WorkerFile{Path: "worker.js", Content: `
import Engine from "./index.js";
export * from "./index.js";
export default class extends Engine {
  fetch(request) { return super.fetch(request); }
}`})
	var assets []nanoflare.AssetFile
	assetsDir := filepath.Join(artifact, "public")
	if err := filepath.WalkDir(assetsDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(assetsDir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		assets = append(assets, nanoflare.AssetFile{Path: filepath.ToSlash(relative), Data: data})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = service.Deploy(app.ID, nanoflare.DeployInput{
		CompatibilityDate:  "2026-09-11",
		CompatibilityFlags: []string{"global_fetch_strictly_public"},
		Format:             "modules", Entrypoint: "worker.js",
		WorkerLoaders: []nanoflare.WorkerLoaderBinding{{Binding: "LOADER"}},
		Vars:          map[string]json.RawMessage{"KITESURF_ENV": json.RawMessage(`"production"`)},
		AssetConfig:   nanoflare.AssetConfig{Binding: "FONT_ASSETS"},
		Files:         files, Assets: assets,
	})
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(api.NewServerWithRuntime(service, nil, "", nil, nil, manager))
	defer gateway.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "node", "testdata/kitesurf-cdp-smoke.mjs", gateway.URL+"/internal/http/workers/"+app.ID).CombinedOutput()
	if err != nil {
		t.Fatalf("CDP: %v: %s", err, out)
	}
	t.Log(string(out))
}
