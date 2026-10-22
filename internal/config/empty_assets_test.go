package config

import (
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestLoaderPreservesEmptyExplicitAssetBinding(t *testing.T) {
	a := nanoflare.ActiveDeployment{Deployment: nanoflare.Deployment{Entrypoint: "worker.js", Format: "modules", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {fetch(r,e){return e.STATIC.fetch(r)}}`}}, AssetConfig: nanoflare.AssetConfig{Binding: "STATIC"}}}
	code := InstrumentLoaderInvocation(SnapshotWorkerCodeForLoader(a, WorkerdOptions{}), a)
	if !strings.Contains(code.Modules["__nanoflare_imported_env__.js"].JS, `"fetch":["STATIC"]`) {
		t.Fatal("explicit STATIC binding omitted from loader environment when assets are empty")
	}
}
