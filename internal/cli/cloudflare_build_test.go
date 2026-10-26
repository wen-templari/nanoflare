package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloudflareBuild(t *testing.T) {
	for _, scenario := range []string{"valid", "stale", "auxiliary", "missing-entry", "escape", "invalid-pointer", "binary", "different-config", "missing-output"} {
		t.Run(scenario, func(t *testing.T) {
			withWorkingDirectory(t, t.TempDir())
			write := func(path, content string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			root, _ := os.Getwd()
			write("wrangler.jsonc", `{"name":"poc","main":"src/worker.ts","compatibility_date":"2026-10-01","vars":{"MESSAGE":"source"},"nanoflare":{"auth":{"protected_routes":["/admin/*"]}},"assets":{"binding":"ASSETS"}}`)
			write("src/worker.ts", `export default { fetch() { return new Response("source") } }`)
			config := map[string]any{"name": "poc", "main": "index.js", "configPath": filepath.Join(root, "wrangler.jsonc"), "no_bundle": true, "durable_objects": map[string]any{"bindings": []any{}}, "assets": map[string]string{"directory": "../client"}}
			pointer := `{"configPath":"../../dist/poc/wrangler.json","auxiliaryWorkers":[]}`
			want := ""
			switch scenario {
			case "different-config":
				config["configPath"] = filepath.Join(root, "other.jsonc")
				want = "different source config"
			case "missing-output":
				pointer = `{"configPath":"../../dist/missing/wrangler.json"}`
				want = "run vite build"
			case "stale":
				config["name"] = "other"
				want = "does not match"
			case "auxiliary":
				pointer = `{"configPath":"../../dist/poc/wrangler.json","auxiliaryWorkers":[{}]}`
				want = "auxiliary"
			case "escape":
				config["main"] = "../../../outside.js"
				want = "inside the project"
			case "missing-entry":
				want = "entry is missing"
			case "invalid-pointer":
				pointer = `{`
				want = "pointer"
			case "binary":
				write("dist/poc/module.wasm", "binary")
				want = "binary module"
			}
			encoded, _ := json.Marshal(config)
			write("dist/poc/wrangler.json", string(encoded))
			write(".wrangler/deploy/config.json", pointer)
			if scenario != "missing-entry" {
				write("dist/poc/index.js", `import {message} from "./chunks/message.js"; export default {fetch(){return new Response(message)}}`)
			}
			write("dist/poc/chunks/message.js", `export const message="built"`)
			write("dist/poc/.vite/manifest.json", `{}`)
			write("dist/client/index.html", "built client")
			_, project, err := loadDeploymentProject()
			if want != "" {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %v; want %s", err, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if project.Main != "dist/poc/index.js" || project.Assets.Directory != "dist/client" || project.Assets.Binding != "ASSETS" || len(project.Files) != 2 || string(project.Vars["MESSAGE"]) != `"source"` || len(project.Auth.ProtectedRoutes) != 1 {
				t.Fatalf("project = %#v", project)
			}
			files, err := projectWorkerFiles(project)
			if err != nil || len(files) != 2 {
				t.Fatalf("files = %#v, err = %v", files, err)
			}
		})
	}
}
