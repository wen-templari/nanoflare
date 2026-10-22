package api

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/clas/nanoflare/internal/database"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestDispatchResourceSharingRevisionAndRevocationMatrix(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var repo nanoflare.Repository = nanoflare.NewStore()
			if backend == "postgres" {
				repo = runtimePostgres(t)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(repo.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org", UsageLevel: nanoflare.UsageLevelPaid}))
			service := nanoflare.NewServiceWithObjects(repo, discardWriter{}, newAPIObjectStore())
			service.SetPlatformDispatchEnabled(true)
			sqlite, err := database.NewSQLiteManager(t.TempDir(), nil)
			must(err)
			service.SetDBExecutor(sqlite)
			ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "matrix", CustomerMode: "platform"})
			must(err)
			alpha, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", CustomerID: "a", Execution: "dynamic"})
			must(err)
			beta, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "beta", CustomerID: "b", Execution: "dynamic"})
			must(err)
			type resources struct{ kv, db, bucket string }
			makeResources := func(name string) resources {
				t.Helper()
				kv, e := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: "org", Name: name})
				must(e)
				db, e := service.CreateDatabase(nanoflare.CreateDatabaseInput{OrgID: "org", Name: name})
				must(e)
				bucket, e := service.CreateObjectStorageBucket(nanoflare.CreateObjectStorageBucketInput{OrgID: "org", Name: name})
				must(e)
				return resources{kv.ID, db.ID, bucket.ID}
			}
			oldResources, newResources, shared := makeResources("old"), makeResources("new"), makeResources("shared")
			var grants []nanoflare.NamespaceResourceGrant
			addGrants := func(r resources, customer string) {
				for kind, id := range map[string]string{"kv": r.kv, "db": r.db, "objects": r.bucket} {
					grants = append(grants, nanoflare.NamespaceResourceGrant{Kind: kind, ResourceID: id, CustomerID: customer})
				}
			}
			addGrants(oldResources, "a")
			addGrants(newResources, "a")
			addGrants(shared, "a")
			addGrants(shared, "b")
			ns, err = service.SetNamespaceGrants("org", ns.ID, nanoflare.NamespaceGrantsInput{ExpectedRevision: 1, Grants: grants})
			must(err)
			input := func(r resources, version string) nanoflare.DeployInput {
				return nanoflare.DeployInput{Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}, KVNamespaces: []nanoflare.KVBinding{{Binding: "OWN_KV", ID: r.kv}, {Binding: "SHARED_KV", ID: shared.kv}}, Databases: []nanoflare.DatabaseBinding{{Binding: "OWN_DB", DatabaseID: r.db}, {Binding: "SHARED_DB", DatabaseID: shared.db}}, ObjectStorageBuckets: []nanoflare.ObjectStorageBucketBinding{{Binding: "OWN_BUCKET", BucketID: r.bucket}, {Binding: "SHARED_BUCKET", BucketID: shared.bucket}}, Assets: []nanoflare.AssetFile{{Path: "snapshot.txt", Data: []byte(version)}}}
			}
			first, err := service.Deploy(alpha.ID, input(oldResources, "old"))
			must(err)
			betaInput := input(shared, "beta")
			betaInput.KVNamespaces = betaInput.KVNamespaces[1:]
			betaInput.Databases = betaInput.Databases[1:]
			betaInput.ObjectStorageBuckets = betaInput.ObjectStorageBuckets[1:]
			_, err = service.Deploy(beta.ID, betaInput)
			must(err)
			deniedInput := input(oldResources, "denied")
			if _, err = service.Deploy(beta.ID, deniedInput); !errors.Is(err, nanoflare.ErrNamespaceGrantDenied) {
				t.Fatalf("customer deployment acquired private grants: %v", err)
			}
			platform, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "platform", Hostname: "matrix.example.com"})
			must(err)
			sourceInput := input(shared, "host")
			sourceInput.KVNamespaces = nil
			sourceInput.Databases = nil
			sourceInput.ObjectStorageBuckets = nil
			sourceInput.DispatchNamespaces = []nanoflare.DispatchBinding{{Binding: "DISPATCHER", NamespaceID: ns.ID}}
			sourceDeployment, err := service.Deploy(platform.ID, sourceInput)
			must(err)
			token := nanoflare.RuntimeCapability(nanoflare.ActiveDeployment{App: platform, Deployment: sourceDeployment}, nanoflare.RuntimeDispatch, ns.ID)
			ctx := context.Background()
			old, err := service.AcquireDispatch(ctx, token, ns.ID, "alpha")
			must(err)
			second, err := service.Deploy(alpha.ID, input(newResources, "new"))
			must(err)
			newer, err := service.AcquireDispatch(ctx, token, ns.ID, "alpha")
			must(err)
			other, err := service.AcquireDispatch(ctx, token, ns.ID, "beta")
			must(err)
			// Requests running together keep exact bindings and assets from their revision.
			var wg sync.WaitGroup
			for range 8 {
				for _, item := range []struct {
					lease          string
					own, wrong     resources
					revision, body string
				}{{old.Lease, oldResources, newResources, first.ID, "old"}, {newer.Lease, newResources, oldResources, second.ID, "new"}} {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for kind, id := range map[string]string{"kv": item.own.kv, "db": item.own.db, "objects": item.own.bucket} {
							if _, e := service.AuthorizeRuntime(ctx, item.lease, kind, id); e != nil {
								t.Error(e)
							}
						}
						for kind, id := range map[string]string{"kv": item.wrong.kv, "db": item.wrong.db, "objects": item.wrong.bucket} {
							if _, e := service.AuthorizeRuntime(ctx, item.lease, kind, id); !errors.Is(e, nanoflare.ErrRuntimeGrantDenied) {
								t.Errorf("revision crossed %s: %v", kind, e)
							}
						}
						asset, e := service.AssetFetch(item.lease, item.revision, "/snapshot.txt")
						if e != nil || string(asset.Body) != item.body {
							t.Errorf("mixed asset snapshot: %q %v", asset.Body, e)
						}
					}()
				}
			}
			wg.Wait()
			if _, err = service.AssetFetch(old.Lease, second.ID, "/snapshot.txt"); !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatalf("foreign artifact accepted: %v", err)
			}
			// Deliberate sharing gives both customers the same KV, SQLite file and bucket.
			must(service.KVPut(old.Lease, shared.kv, "shared", []byte("value")))
			value, found, err := service.KVGet(other.Lease, shared.kv, "shared")
			must(err)
			if !found || string(value) != "value" {
				t.Fatal("shared KV mismatch")
			}
			_, err = service.DBExecute(old.Lease, shared.db, nanoflare.DBQueryRequest{Method: "exec", Statements: []nanoflare.DBStatementRequest{{SQL: `CREATE TABLE shared(value);INSERT INTO shared VALUES('value')`}}})
			must(err)
			result, err := service.DBExecute(other.Lease, shared.db, nanoflare.DBQueryRequest{Method: "first", FirstColumn: "value", Statements: []nanoflare.DBStatementRequest{{SQL: `SELECT value FROM shared`}}})
			must(err)
			if result.First != "value" {
				t.Fatal("shared database mismatch")
			}
			_, err = service.ObjectPut(old.Lease, shared.bucket, "shared.txt", "text/plain", []byte("value"))
			must(err)
			object, found, err := service.ObjectGet(other.Lease, shared.bucket, "shared.txt")
			must(err)
			if !found || string(object.Body) != "value" {
				t.Fatal("shared object bucket mismatch")
			}
			for kind, id := range map[string]string{"kv": oldResources.kv, "db": oldResources.db, "objects": oldResources.bucket} {
				if _, err = service.AuthorizeRuntime(ctx, other.Lease, kind, id); !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
					t.Fatalf("customer crossed resource %s: %v", kind, err)
				}
			}

			_, err = service.ObjectPut(old.Lease, oldResources.bucket, "guard.txt", "text/plain", []byte("private"))
			must(err)
			escape := "../" + oldResources.bucket + "/guard.txt"
			_, found, err = service.ObjectGet(other.Lease, shared.bucket, escape)
			must(err)
			if found {
				t.Fatal("object key escaped the authorized bucket")
			}
			must(service.DeleteObject(other.Lease, shared.bucket, escape))
			_, err = service.ObjectPut(other.Lease, shared.bucket, escape, "text/plain", []byte("attacker"))
			must(err)
			private, found, err := service.ObjectGet(old.Lease, oldResources.bucket, "guard.txt")
			must(err)
			if !found || string(private.Body) != "private" {
				t.Fatal("object traversal changed private data")
			}
			// Current revocation overrides every pinned revision, without affecting the
			// second customer's explicit sharing grant or revision-local static assets.
			var retained []nanoflare.NamespaceResourceGrant
			for _, grant := range grants {
				if grant.CustomerID == "b" {
					retained = append(retained, grant)
				}
			}
			_, err = service.SetNamespaceGrants("org", ns.ID, nanoflare.NamespaceGrantsInput{ExpectedRevision: ns.PolicyRevision, Grants: retained})
			must(err)
			for _, lease := range []string{old.Lease, newer.Lease} {
				for kind, id := range map[string]string{"kv": shared.kv, "db": shared.db, "objects": shared.bucket} {
					if _, err = service.AuthorizeRuntime(ctx, lease, kind, id); !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
						t.Fatalf("revoked %s accepted: %v", kind, err)
					}
				}
			}
			for kind, id := range map[string]string{"kv": shared.kv, "db": shared.db, "objects": shared.bucket} {
				_, err = service.AuthorizeRuntime(ctx, other.Lease, kind, id)
				must(err)
			}
			if err = service.KVPut(old.Lease, shared.kv, "revoked", []byte("denied")); !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatalf("revoked KV write: %v", err)
			}
			if _, err = service.DBExecute(old.Lease, shared.db, nanoflare.DBQueryRequest{Method: "exec", Statements: []nanoflare.DBStatementRequest{{SQL: `DROP TABLE shared`}}}); !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatalf("revoked database effect: %v", err)
			}
			if _, _, err = service.ObjectGet(old.Lease, shared.bucket, "shared.txt"); !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatalf("revoked object read: %v", err)
			}
			_, err = service.AssetFetch(old.Lease, first.ID, "/snapshot.txt")
			must(err)
		})
	}
}
