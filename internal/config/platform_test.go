package config

import (
	"github.com/clas/nanoflare/internal/nanoflare"
	"strings"
	"testing"
)

func TestPrivateScriptsHaveNoPublicSocketsOrRoutes(t *testing.T) {
	item := nanoflare.ActiveDeployment{App: nanoflare.App{ID: "private-worker", Platform: &nanoflare.WorkerPlatform{NamespaceID: "ns", ScriptName: "alpha", Kind: "customer", Exposure: "private"}}, Deployment: nanoflare.Deployment{ID: "revision", AppID: "private-worker", Port: 9012, Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}}}
	if strings.Contains(Workerd([]nanoflare.ActiveDeployment{item}), "*:9012") {
		t.Fatal("private Worker received a public TCP socket")
	}
	item.SocketPath = "/tmp/private-worker.sock"
	if !strings.Contains(Workerd([]nanoflare.ActiveDeployment{item}), "unix:/tmp/private-worker.sock") {
		t.Fatal("private runner socket missing")
	}
	if strings.Contains(Traefik([]nanoflare.ActiveDeployment{item}, "http://control/internal/auth", "", "runtime"), "private-worker") {
		t.Fatal("private Worker exposed through Traefik")
	}
}
