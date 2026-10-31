package nanoflare

import (
	"strings"
	"testing"
)

func TestOrdinaryScriptLongNameDefaultHostname(t *testing.T) {
	for _, base := range []string{"workers.example.test", "*-nanoflare.example.test"} {
		t.Run(base, func(t *testing.T) {
			store := NewStore()
			if err := store.CreateOrganization(Organization{ID: "org-long", Name: strings.Repeat("organization", 8)}); err != nil {
				t.Fatal(err)
			}
			service := NewService(store, &recordingWriter{})
			if err := service.SetBaseHostname(base); err != nil {
				t.Fatal(err)
			}
			name := strings.Repeat("a", 63)
			app, err := service.EnsureOrdinaryScriptForOrg("org-long", name, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := normalizeHostname(app.Hostname); err != nil || app.Hostname == "" || app.Name != name {
				t.Fatalf("invalid generated identity: %#v, %v", app, err)
			}
			// A second long name with the same retained prefix must stay distinct.
			other, err := service.EnsureOrdinaryScriptForOrg("org-long", strings.Repeat("a", 62)+"b", "")
			if err != nil || other.Hostname == app.Hostname {
				t.Fatalf("hostname collision: %q, %v", other.Hostname, err)
			}
			again, err := service.EnsureOrdinaryScriptForOrg("org-long", name, "")
			if err != nil || again.Hostname != app.Hostname || again.ID != app.ID {
				t.Fatalf("identity changed: %#v, %v", again, err)
			}
		})
	}
}
