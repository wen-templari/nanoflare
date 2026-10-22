package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/lib/pq"
)

func TestPlatformMigrationPreservesLegacyWorkersAndDeployments(t *testing.T) {
	url := os.Getenv("NANOFLARE_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("NANOFLARE_TEST_POSTGRES_URL is not configured")
	}
	admin, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("platform_migration_%d", time.Now().UnixNano())
	if _, err = admin.Exec("CREATE SCHEMA " + pq.QuoteIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP SCHEMA " + pq.QuoteIdentifier(schema) + " CASCADE")
	dsn, err := pq.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	dsn += " search_path=" + schema
	repo, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	original := nanoflare.App{ID: "legacy", OrgID: "org", Name: "same-name", Hostname: "legacy.example.com", Auth: nanoflare.AuthConfig{ProtectedRoutes: []string{"/admin"}}, RuntimeToken: "legacy-root", CreatedAt: now}
	must(repo.CreateApp(original))
	must(repo.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org"}))
	oldOwner := []string{}
	for _, scope := range nanoflare.RoleScopes(nanoflare.RoleOwner) {
		if len(scope) < 11 || scope[:11] != "namespaces:" {
			oldOwner = append(oldOwner, scope)
		}
	}
	for _, id := range []string{"default-owner", "restricted-owner"} {
		must(repo.CreateUser(nanoflare.User{ID: id, Email: id + "@example.com", CreatedAt: now}))
		scopes := oldOwner
		if id == "restricted-owner" {
			scopes = []string{"workers:read"}
		}
		must(repo.UpsertOrganizationMembership(nanoflare.OrganizationMembership{UserID: id, OrgID: "org", Role: nanoflare.RoleOwner, Scopes: scopes, CreatedAt: now}))
	}
	second := original
	second.ID = "other-legacy"
	second.RuntimeToken = "other-root"
	second.Hostname = "other.example.com"
	must(repo.CreateApp(second))
	dep := nanoflare.Deployment{ID: "legacy-revision", AppID: original.ID, Entrypoint: "worker.js", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}, Format: "modules", CompatibilityDate: "2025-12-10", CreatedAt: now, Port: 9001}
	must(repo.Activate(dep))
	before, err := repo.ActiveDeployments()
	must(err)
	// Reconstruct the previous schema inside this test's isolated schema. The
	// original worker/deployment rows and credentials remain untouched.
	_, err = repo.db.Exec(`DROP TABLE asset_upload_sessions;DROP VIEW dispatch_legacy_name_conflicts;ALTER TABLE workers DROP COLUMN platform_namespace_id;ALTER TABLE workers DROP COLUMN platform;DROP TABLE dispatch_namespace_policies;DROP TABLE dispatch_namespaces;`)
	must(err)
	must(repo.Close())
	repo, err = Open(context.Background(), dsn)
	must(err)
	defer repo.Close()
	after, err := repo.ActiveDeployments()
	must(err)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed live revisions: before=%#v after=%#v", before, after)
	}
	got, err := repo.GetApp(original.ID)
	must(err)
	if !reflect.DeepEqual(got, original) || got.IsPrivate() {
		t.Fatalf("legacy identity changed: %#v", got)
	}
	namespaces, err := repo.ListDispatchNamespaces("org")
	must(err)
	if len(namespaces) != 0 {
		t.Fatal("legacy workers automatically assigned to namespaces")
	}
	var conflicts int
	must(repo.db.QueryRow(`SELECT count(*) FROM dispatch_legacy_name_conflicts WHERE org_id='org' AND name='same-name'`).Scan(&conflicts))
	if conflicts != 1 {
		t.Fatalf("duplicate legacy names not diagnosed: %d", conflicts)
	}
	// Partial hostname uniqueness still protects public Workers after migration.
	duplicate := original
	duplicate.ID = "duplicate"
	if err = repo.CreateApp(duplicate); err != nanoflare.ErrAppExists {
		t.Fatalf("public hostname uniqueness lost: %v", err)
	}
	defaultOwner, err := repo.OrganizationMembership("default-owner", "org")
	must(err)
	if !nanoflare.HasScope(defaultOwner.Scopes, "namespaces:dispatch") {
		t.Fatal("legacy default owner did not gain new role-default namespace permissions")
	}
	restricted, err := repo.OrganizationMembership("restricted-owner", "org")
	must(err)
	if nanoflare.HasScope(restricted.Scopes, "namespaces:dispatch") {
		t.Fatal("custom restrictions silently expanded")
	}
	must(repo.migratePlatform(context.Background()))
}
