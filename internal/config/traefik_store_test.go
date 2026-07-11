package config

import (
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestTraefikStorePublishesGeneratedConfiguration(t *testing.T) {
	store := NewTraefikStore("http://nanoflared/internal/auth/verify", "runtime.internal")
	if err := store.WriteTraefik([]nanoflare.ActiveDeployment{{
		App:        nanoflare.App{ID: "hello-app", Hostname: "hello.example.com"},
		Deployment: nanoflare.Deployment{Port: 10001},
	}}); err != nil {
		t.Fatal(err)
	}
	config := string(store.TraefikConfig())
	for _, expected := range []string{
		`rule: "Host(` + "`" + `hello.example.com` + "`" + `)"`,
		`- web`,
		`- websecure`,
		`prefix: "/internal/http/apps/hello-app"`,
		`url: "http://nanoflared"`,
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config does not contain %q:\n%s", expected, config)
		}
	}
}
