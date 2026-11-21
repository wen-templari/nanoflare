package cli

import (
	"strings"
	"testing"
)

func TestWorkerLoadersWranglerConfigAndTypes(t *testing.T) {
	for path, document := range map[string]string{
		"wrangler.jsonc": `{"name":"test","main":"index.js","compatibility_date":"2026-09-08","worker_loaders":[{"binding":"LOADER"}]}`,
		"wrangler.toml": `
name = "test"
main = "index.js"
compatibility_date = "2026-09-08"
[[worker_loaders]]
binding = "LOADER"
`,
	} {
		t.Run(path, func(t *testing.T) {
			project, err := parseProjectConfig(path, []byte(document), false)
			if err != nil {
				t.Fatal(err)
			}
			if len(project.WorkerLoaders) != 1 || project.WorkerLoaders[0].Binding != "LOADER" {
				t.Fatalf("worker loaders = %#v", project.WorkerLoaders)
			}
			declarations, err := generateWorkerTypes(project, "Env")
			if err != nil || !strings.Contains(string(declarations), "LOADER: NanoflareWorkerLoader;") {
				t.Fatalf("binding type missing: %v", err)
			}
		})
	}
}
