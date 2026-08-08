// Package templates contains the project templates embedded in the CLI.
package templates

import (
	"embed"
	"io/fs"
)

// ProjectConfig describes the project settings supplied by a template. It is
// independent from the CLI's Project type so templates can remain reusable as
// the CLI configuration grows.
type ProjectConfig struct {
	Main                 string
	Format               string
	Files                []string
	KVNamespaces         []Binding
	ObjectStorageBuckets []BucketBinding
	Databases            []DatabaseBinding
	Assets               *AssetsConfig
}

type Binding struct {
	Binding string
	ID      string
}
type BucketBinding struct {
	Binding  string
	BucketID string
}
type DatabaseBinding struct {
	Binding    string
	DatabaseID string
}
type AssetsConfig struct {
	Binding, Directory, HTMLHandling, NotFoundHandling string
	RunWorkerFirst                                     []string
}

// Template is a named, embedded project skeleton. Files contains source files
// only; the CLI creates nanoflare.json from Project.
type Template struct {
	ID          string
	Description string
	Files       fs.FS
	Root        string
	Project     func() ProjectConfig
}

//go:embed all:starter-worker all:bindings-worker all:pages-app all:spa-app all:ssr-app all:api-worker
var embeddedFiles embed.FS

// Catalog is the built-in template catalog. IDs are part of the public CLI
// interface for non-interactive initialization.
var Catalog = []Template{
	{
		ID:          "starter",
		Description: "A minimal TypeScript Worker",
		Files:       embeddedFiles,
		Root:        "starter-worker",
		Project: func() ProjectConfig {
			return ProjectConfig{
				Main:   "worker.ts",
				Format: "modules",
			}
		},
	},
	{ID: "bindings", Description: "A Hono Worker with KV and object storage", Files: embeddedFiles, Root: "bindings-worker", Project: func() ProjectConfig {
		return ProjectConfig{Main: "src/worker.ts", Format: "modules", KVNamespaces: []Binding{{Binding: "KV", ID: "replace-with-kv-namespace-id"}}, ObjectStorageBuckets: []BucketBinding{{Binding: "OBJECTS", BucketID: "replace-with-object-storage-bucket-id"}}}
	}},
	{ID: "pages", Description: "A Vite and Tailwind static site", Files: embeddedFiles, Root: "pages-app", Project: func() ProjectConfig {
		return ProjectConfig{Main: "dist/worker.js", Format: "modules", Files: []string{"dist/worker.js"}, Assets: &AssetsConfig{Binding: "ASSETS", Directory: "dist/client", HTMLHandling: "auto-trailing-slash"}}
	}},
	{ID: "spa", Description: "A React SPA with a Worker API route", Files: embeddedFiles, Root: "spa-app", Project: func() ProjectConfig {
		return ProjectConfig{Main: "dist/worker.js", Format: "modules", Files: []string{"dist/worker.js"}, Assets: &AssetsConfig{Binding: "ASSETS", Directory: "dist/client", NotFoundHandling: "single-page-application", RunWorkerFirst: []string{"/api/*"}}}
	}},
	{ID: "ssr", Description: "A React SSR app with Hono", Files: embeddedFiles, Root: "ssr-app", Project: func() ProjectConfig {
		return ProjectConfig{Main: "src/worker.tsx", Format: "modules"}
	}},
	{ID: "api", Description: "A documented Hono and Drizzle API", Files: embeddedFiles, Root: "api-worker", Project: func() ProjectConfig {
		return ProjectConfig{Main: "src/worker.ts", Format: "modules", Databases: []DatabaseBinding{{Binding: "DB", DatabaseID: "replace-with-database-id"}}}
	}},
}
