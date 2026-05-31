package config

import (
	"strings"
	"testing"

	"github.com/clas/platform/internal/platform"
)

func TestTraefikStorePublishesGeneratedConfiguration(t *testing.T) {
	store := NewTraefikStore("http://platformd/internal/auth/verify", "runtime.internal")
	if err := store.WriteTraefik([]platform.ActiveDeployment{{
		App:        platform.App{ID: "hello-app", Hostname: "hello.example.com"},
		Deployment: platform.Deployment{Port: 10001},
	}}); err != nil {
		t.Fatal(err)
	}
	config := string(store.TraefikConfig())
	for _, expected := range []string{
		`rule: "Host(` + "`" + `hello.example.com` + "`" + `)"`,
		`url: "http://runtime.internal:10001/"`,
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config does not contain %q:\n%s", expected, config)
		}
	}
}
