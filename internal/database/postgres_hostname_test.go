package database

import (
	"database/sql"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

// Set NANOFLARE_TEST_POSTGRES_URL to run against PostgreSQL. A connection-local
// temporary workers table isolates this test from persistent application data.
func TestPostgresPatchAppPreservesOmissionsAndUniqueness(t *testing.T) {
	url := os.Getenv("NANOFLARE_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("NANOFLARE_TEST_POSTGRES_URL is not configured")
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	_, err = db.Exec(`CREATE TEMP TABLE workers (
 id text PRIMARY KEY, org_id text NOT NULL, name text NOT NULL,
 hostname text NOT NULL UNIQUE, auth jsonb NOT NULL,
 external_id text NOT NULL, oauth_client_id text NOT NULL, created_by text NOT NULL,
 runtime_token text NOT NULL, created_at timestamptz NOT NULL, platform jsonb NOT NULL DEFAULT 'null')`)
	if err != nil {
		t.Fatal(err)
	}
	repo := &Postgres{db: db}
	app := nanoflare.App{ID: "site", OrgID: "org", Name: "Site", Hostname: "old.example.com", ExternalID: "external", OAuthClientID: "client", CreatedBy: "creator", RuntimeToken: "runtime-secret", CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	if err := repo.CreateApp(app); err != nil {
		t.Fatal(err)
	}
	other := app
	other.ID = "other"
	other.OrgID = "other-org"
	other.Hostname = "taken.example.com"
	if err := repo.CreateApp(other); err != nil {
		t.Fatal(err)
	}
	hostname := "new.example.com"
	auth := nanoflare.AuthConfig{ProtectedRoutes: []string{"/private"}}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, input := range []nanoflare.UpdateAppInput{{Hostname: &hostname}, {Auth: &auth}} {
		wg.Add(1)
		go func(input nanoflare.UpdateAppInput) {
			defer wg.Done()
			<-start
			if _, err := repo.PatchApp(app.ID, input); err != nil {
				t.Error(err)
			}
		}(input)
	}
	close(start)
	wg.Wait()
	updated, err := repo.PatchApp(app.ID, nanoflare.UpdateAppInput{})
	if err != nil {
		t.Fatal(err)
	}
	expected := app
	expected.Hostname = hostname
	expected.Auth = auth
	if !reflect.DeepEqual(updated, expected) {
		t.Fatalf("updated=%#v, expected=%#v", updated, expected)
	}
	same, err := repo.PatchApp(app.ID, nanoflare.UpdateAppInput{Hostname: &hostname})
	if err != nil || !reflect.DeepEqual(same, expected) {
		t.Fatalf("same-hostname update=%#v, %v", same, err)
	}
	conflict := other.Hostname
	if _, err := repo.PatchApp(app.ID, nanoflare.UpdateAppInput{Hostname: &conflict, Auth: &nanoflare.AuthConfig{}}); !errors.Is(err, nanoflare.ErrAppExists) {
		t.Fatalf("conflict=%v", err)
	}
	saved, err := repo.GetApp(app.ID)
	if err != nil || !reflect.DeepEqual(saved, expected) {
		t.Fatalf("conflict changed worker=%#v, %v", saved, err)
	}
	if _, err := repo.PatchApp("missing", nanoflare.UpdateAppInput{Hostname: &hostname}); !errors.Is(err, nanoflare.ErrAppNotFound) {
		t.Fatalf("missing=%v", err)
	}
}
