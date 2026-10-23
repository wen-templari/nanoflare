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

func openStorageTestPostgres(t *testing.T) *Postgres {
	t.Helper()
	url := os.Getenv("NANOFLARE_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("NANOFLARE_TEST_POSTGRES_URL is not configured")
	}
	admin, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	schema := fmt.Sprintf("storage_delete_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + pq.QuoteIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec("DROP SCHEMA " + pq.QuoteIdentifier(schema) + " CASCADE") })
	dsn, err := pq.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := Open(context.Background(), dsn+" search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	return repo
}

func storageTestDeployment(id string) nanoflare.Deployment {
	return nanoflare.Deployment{
		ID: id, AppID: "worker", Entrypoint: "worker.js", Format: "modules",
		CompatibilityDate: "2025-12-10", CreatedAt: time.Now().UTC(),
		Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}},
	}
}

func TestPostgresStorageDeletionBindingLists(t *testing.T) {
	for _, resource := range []struct {
		name     string
		column   string
		key      string
		create   func(*Postgres, string) error
		delete   func(*Postgres, string) error
		inUse    error
		notFound error
	}{
		{"database", "db", "database_id", func(p *Postgres, id string) error {
			return p.CreateDatabase(nanoflare.Database{ID: id, Name: id, CreatedAt: time.Now().UTC()})
		}, (*Postgres).DeleteDatabase, nanoflare.ErrDatabaseInUse, nanoflare.ErrDatabaseNotFound},
		{"kv", "kv_namespaces", "id", func(p *Postgres, id string) error {
			return p.CreateKVNamespace(nanoflare.KVNamespace{ID: id, Name: id, CreatedAt: time.Now().UTC()})
		}, (*Postgres).DeleteKVNamespace, nanoflare.ErrKVNamespaceInUse, nanoflare.ErrKVNamespaceNotFound},
		{"bucket", "object_storage_bucket", "bucket_id", func(p *Postgres, id string) error {
			return p.CreateObjectStorageBucket(nanoflare.ObjectStorageBucket{ID: id, Name: id, CreatedAt: time.Now().UTC()})
		}, (*Postgres).DeleteObjectStorageBucket, nanoflare.ErrObjectStorageBucketInUse, nanoflare.ErrObjectStorageBucketNotFound},
	} {
		t.Run(resource.name, func(t *testing.T) {
			repo := openStorageTestPostgres(t)
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(repo.CreateApp(nanoflare.App{ID: "worker", Name: "worker", Hostname: "worker.example.com", CreatedAt: time.Now().UTC()}))
			for i, bindings := range []string{"null", "[]", fmt.Sprintf(`[{%q:"unrelated"}]`, resource.key)} {
				id := fmt.Sprintf("unrelated-%d", i)
				must(repo.Activate(storageTestDeployment(id)))
				// Simulate existing rows written before binding normalization.
				_, err := repo.db.Exec("UPDATE deployments SET "+pq.QuoteIdentifier(resource.column)+"=$1::jsonb WHERE id=$2", bindings, id)
				must(err)
			}
			must(resource.create(repo, "unused"))
			must(resource.delete(repo, "unused"))
			if err := resource.delete(repo, "unused"); !errors.Is(err, resource.notFound) {
				t.Fatalf("deleted resource: got %v, want %v", err, resource.notFound)
			}

			must(resource.create(repo, "referenced"))
			must(repo.Activate(storageTestDeployment("referencing")))
			_, err := repo.db.Exec("UPDATE deployments SET "+pq.QuoteIdentifier(resource.column)+"=$1::jsonb WHERE id='referencing'", fmt.Sprintf(`[{%q:"referenced"}]`, resource.key))
			must(err)
			for _, active := range []bool{true, false} {
				if !active {
					must(repo.Activate(storageTestDeployment("latest")))
				}
				if err := resource.delete(repo, "referenced"); !errors.Is(err, resource.inUse) {
					t.Fatalf("referenced resource (active=%v): got %v, want %v", active, err, resource.inUse)
				}
			}
			must(repo.DeleteDeployment("referencing"))
			must(resource.delete(repo, "referenced"))
		})
	}
}

func TestPostgresDeploymentStorageBindingPersistence(t *testing.T) {
	repo := openStorageTestPostgres(t)
	if err := repo.CreateApp(nanoflare.App{ID: "worker", Name: "worker", Hostname: "worker.example.com", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"nil", "empty", "populated"} {
		t.Run(state, func(t *testing.T) {
			deployment := storageTestDeployment(state)
			switch state {
			case "empty":
				deployment.Databases = []nanoflare.DatabaseBinding{}
				deployment.KVNamespaces = []nanoflare.KVBinding{}
				deployment.ObjectStorageBuckets = []nanoflare.ObjectStorageBucketBinding{}
			case "populated":
				deployment.Databases = []nanoflare.DatabaseBinding{{Binding: "DB", DatabaseID: "database"}}
				deployment.KVNamespaces = []nanoflare.KVBinding{{Binding: "KV", ID: "namespace"}}
				deployment.ObjectStorageBuckets = []nanoflare.ObjectStorageBucketBinding{{Binding: "BUCKET", BucketID: "bucket"}}
			}
			if err := repo.Activate(deployment); err != nil {
				t.Fatal(err)
			}
			for _, field := range []struct {
				column string
				value  any
			}{
				{"db", deployment.Databases},
				{"kv_namespaces", deployment.KVNamespaces},
				{"object_storage_bucket", deployment.ObjectStorageBuckets},
			} {
				want := "[]"
				if state == "populated" {
					want = string(mustJSON(field.value))
				}
				var matches bool
				if err := repo.db.QueryRow("SELECT "+pq.QuoteIdentifier(field.column)+"=$1::jsonb FROM deployments WHERE id=$2", want, deployment.ID).Scan(&matches); err != nil {
					t.Fatal(err)
				}
				if !matches {
					t.Errorf("%s did not persist %s", field.column, want)
				}
			}
		})
	}
}
