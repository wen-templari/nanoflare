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
	Main   string
	Format string
	Files  []string
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

//go:embed starter-worker/worker.js
var embeddedFiles embed.FS

// Catalog is the built-in template catalog. IDs are part of the public CLI
// interface for non-interactive initialization.
var Catalog = []Template{
	{
		ID:          "starter",
		Description: "A basic JavaScript Worker",
		Files:       embeddedFiles,
		Root:        "starter-worker",
		Project: func() ProjectConfig {
			return ProjectConfig{
				Main:   "worker.js",
				Format: "modules",
				Files:  []string{"worker.js"},
			}
		},
	},
}
