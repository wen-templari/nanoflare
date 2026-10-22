package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/lib/pq"
)

func TestPostgresNamespaceBatchDeleteRollsBackEveryWorker(t *testing.T) {
	url := os.Getenv("NANOFLARE_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("NANOFLARE_TEST_POSTGRES_URL is not configured")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	admin, err := sql.Open("postgres", url)
	must(err)
	defer admin.Close()
	schema := fmt.Sprintf("namespace_delete_%d", time.Now().UnixNano())
	_, err = admin.Exec("CREATE SCHEMA " + pq.QuoteIdentifier(schema))
	must(err)
	defer admin.Exec("DROP SCHEMA " + pq.QuoteIdentifier(schema) + " CASCADE")
	dsn, err := pq.ParseURL(url)
	must(err)
	dsn += " search_path=" + schema
	repo, err := Open(context.Background(), dsn)
	must(err)
	defer repo.Close()
	must(repo.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org", UsageLevel: nanoflare.UsageLevelPaid}))
	service := nanoflare.NewService(repo, managementDiscardWriter{})
	service.SetPlatformDispatchEnabled(true)
	ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "staging"})
	must(err)
	apps := []nanoflare.App{}
	revisions := []string{}
	for _, name := range []string{"a", "b"} {
		app, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: name, Execution: "dynamic"})
		must(err)
		d, err := service.DeployForOrg("org", app.ID, nanoflare.DeployInput{Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}})
		must(err)
		apps = append(apps, app)
		revisions = append(revisions, d.ID)
	}
	// A failure after deployment-row deletion must restore every row/artifact,
	// rather than leaving the earlier Worker deleted from a per-item loop.
	_, err = repo.db.Exec(`CREATE FUNCTION reject_namespace_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.id=` + pq.QuoteLiteral(apps[1].ID) + ` THEN RAISE EXCEPTION 'controlled batch failure'; END IF; RETURN OLD; END $$;
 CREATE TRIGGER reject_namespace_delete BEFORE DELETE ON workers FOR EACH ROW EXECUTE FUNCTION reject_namespace_delete();`)
	must(err)
	ids := []string{apps[0].ID, apps[1].ID}
	if err = repo.DeleteNamespaceScripts("org", ns.ID, ids); err == nil {
		t.Fatal("injected delete failure was ignored")
	}
	for index, app := range apps {
		_, err = repo.GetApp(app.ID)
		must(err)
		_, err = repo.GetDeploymentArtifact(app.ID, revisions[index])
		must(err)
		active, err := repo.ActiveDeploymentsForApp(app.ID)
		must(err)
		if len(active) != 1 {
			t.Fatal("failed transaction changed traffic")
		}
	}
	_, err = repo.db.Exec(`DROP TRIGGER reject_namespace_delete ON workers`)
	must(err)
	must(repo.DeleteNamespaceScripts("org", ns.ID, ids))
	for _, app := range apps {
		if _, err = repo.GetApp(app.ID); !errors.Is(err, nanoflare.ErrAppNotFound) {
			t.Fatal("batch deletion did not remove Worker", err)
		}
	}
}
