package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/lib/pq"
)

type managementDiscardWriter struct{}

func (managementDiscardWriter) Write([]nanoflare.ActiveDeployment) error { return nil }

func TestPostgresManagedSnapshotSurvivesConnectionReopen(t *testing.T) {
	url := os.Getenv("NANOFLARE_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("NANOFLARE_TEST_POSTGRES_URL is not configured")
	}
	admin, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("management_reopen_%d", time.Now().UnixNano())
	if _, err = admin.Exec("CREATE SCHEMA " + pq.QuoteIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP SCHEMA " + pq.QuoteIdentifier(schema) + " CASCADE")
	dsn, err := pq.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	dsn += " search_path=" + schema
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	repo, err := Open(context.Background(), dsn)
	must(err)
	defer repo.Close()
	must(repo.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org", UsageLevel: nanoflare.UsageLevelPaid}))
	codec, err := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
	must(err)
	service := nanoflare.NewService(repo, managementDiscardWriter{})
	service.SetSecretCodec(codec)
	app, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "managed-reopen", Hostname: "managed-reopen.example.com"})
	must(err)
	input := nanoflare.ManagedDeployInput{Deployment: nanoflare.DeployInput{IdempotencyKey: "first", Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}}, Metadata: nanoflare.ScriptManagementMetadata{Tags: []string{"persistent"}, Bindings: []json.RawMessage{json.RawMessage(`{"name":"TOKEN","type":"secret_text"}`)}}, Secrets: map[string]string{"TOKEN": "persisted-sealed-value"}}
	first, err := service.ManagedDeployForOrg("org", app.ID, input)
	must(err)
	must(repo.Close())
	// A new database pool reruns migrations and a new Service has no process
	// memory from the original publication. The PostgreSQL server stays running.
	reopened, err := Open(context.Background(), dsn)
	must(err)
	defer reopened.Close()
	restarted := nanoflare.NewService(reopened, managementDiscardWriter{})
	restarted.SetSecretCodec(codec)
	state, found, err := restarted.ScriptManagementForOrg("org", app.ID)
	must(err)
	if !found || state.Revision != 1 || state.DeploymentID != first.ID || state.Metadata.Tags[0] != "persistent" {
		t.Fatal("management state lost after connection reopen")
	}
	input.Deployment.IdempotencyKey = "native-after-reopen"
	second, err := restarted.DeployForOrg("org", app.ID, input.Deployment)
	must(err)
	artifact, err := reopened.GetDeploymentArtifact(app.ID, second.ID)
	must(err)
	if len(artifact.Secrets) != 1 {
		t.Fatal("reopened native deploy lost secret")
	}
	value, err := codec.Decrypt(artifact.Secrets[0].Nonce, artifact.Secrets[0].Ciphertext)
	must(err)
	if value != "persisted-sealed-value" {
		t.Fatal("reopened native deploy used stale secret store")
	}
	state, _, err = restarted.ScriptManagementForOrg("org", app.ID)
	must(err)
	if state.Revision != 2 || state.DeploymentID != second.ID {
		t.Fatal("reopened native deploy lost optimistic metadata")
	}
}
