package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/database"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/lib/pq"
)

type outboundPendingJournal struct {
	nanoflare.Repository
	interrupt bool
}

func (r *outboundPendingJournal) FinishVarsUpdate(app, key, status string) error {
	if r.interrupt && status == "succeeded" {
		r.interrupt = false
		return errors.New("process stopped before outcome persisted")
	}
	return r.Repository.FinishVarsUpdate(app, key, status)
}

type outboundFailingWriter struct{ fail bool }

func (w *outboundFailingWriter) Write(_ []nanoflare.ActiveDeployment) error {
	if w.fail {
		w.fail = false
		return errors.New("injected route publication failure")
	}
	return nil
}

// PostgreSQL closes the connection pool and reopens the same schema; memory
// recreates the service over retained repository state. Neither revives leases.
func TestOutboundPublicationPersistenceRecovery(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			var repo nanoflare.Repository = nanoflare.NewStore()
			reopen := func() nanoflare.Repository { return repo }
			if backend == "postgres" {
				url := os.Getenv("NANOFLARE_TEST_POSTGRES_URL")
				if url == "" {
					t.Skip("disposable PostgreSQL required")
				}
				admin, err := sql.Open("postgres", url)
				must(err)
				schema := fmt.Sprintf("outbound_recovery_%d", time.Now().UnixNano())
				_, err = admin.Exec("CREATE SCHEMA " + pq.QuoteIdentifier(schema))
				must(err)
				dsn, err := pq.ParseURL(url)
				must(err)
				dsn += " search_path=" + schema
				pg, err := database.Open(context.Background(), dsn)
				must(err)
				repo = pg
				reopen = func() nanoflare.Repository {
					must(pg.Close())
					var err error
					pg, err = database.Open(context.Background(), dsn)
					must(err)
					return pg
				}
				t.Cleanup(func() {
					pg.Close()
					admin.Exec("DROP SCHEMA " + pq.QuoteIdentifier(schema) + " CASCADE")
					admin.Close()
				})
			}
			must(repo.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org", UsageLevel: nanoflare.UsageLevelPaid}))
			journal := &outboundPendingJournal{Repository: repo}
			service, _ := managedService(t, journal, discardWriter{})
			ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "customers"})
			must(err)
			policy, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "policy", Hostname: "policy.test"})
			must(err)
			code := nanoflare.DeployInput{Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "import {WorkerEntrypoint} from 'cloudflare:workers';export class Policy extends WorkerEntrypoint {fetch(){return new Response('policy')}};export default {fetch(){return new Response('default')}}"}}}
			_, err = service.Deploy(policy.ID, code)
			must(err)
			host, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "host", Hostname: "host.test"})
			must(err)
			must(service.PutSecret(host.ID, "TOKEN", "original"))
			base, err := service.Deploy(host.ID, code)
			must(err)
			original, err := repo.GetDeploymentArtifact(host.ID, base.ID)
			must(err)
			// Retained publication must preserve the exact frozen secret reference.
			bindings := []nanoflare.DispatchBinding{{Binding: "CUSTOMERS", NamespaceID: ns.ID, Outbound: &nanoflare.DispatchOutbound{WorkerID: policy.ID, Entrypoint: "Policy", Parameters: []string{"context"}}}}
			input := nanoflare.UpdateDispatchBindingsInput{DispatchNamespaces: bindings, ExpectedDeploymentID: base.ID, IdempotencyKey: "recover-outbound"}
			journal.interrupt = true
			_, err = service.UpdateDispatchBindingsForOrg("org", host.ID, input)
			if err == nil || journal.interrupt {
				t.Fatalf("outcome interruption not observed: %v", err)
			}
			repo = reopen()
			restarted, _ := managedService(t, repo, discardWriter{})
			recovered, err := restarted.UpdateDispatchBindingsForOrg("org", host.ID, input)
			must(err)
			active, err := repo.ActiveDeploymentsForApp(host.ID)
			must(err)
			if len(active) != 1 || active[0].Deployment.ID != recovered.ID {
				t.Fatalf("lost active recovered revision: %+v", active)
			}
			// Dispatch metadata is persisted in the immutable artifact, not the
			// legacy deployments table. Check the hydrated production boundary.
			hydrated, _, found, err := restarted.WorkerRuntimeDeployment(host.ID, "/")
			must(err)
			if !found || !reflect.DeepEqual(hydrated.Deployment.DispatchNamespaces, bindings) {
				t.Fatalf("lost hydrated selector/bindings: %+v", hydrated)
			}

			artifact, err := repo.GetDeploymentArtifact(host.ID, recovered.ID)
			must(err)
			if !reflect.DeepEqual(artifact.Secrets, original.Secrets) {
				t.Fatal("retained publication changed frozen secret references")
			}
			replay, err := restarted.UpdateDispatchBindingsForOrg("org", host.ID, input)
			must(err)
			if replay.ID != recovered.ID {
				t.Fatal("retry republished revision")
			}
			// Later publication must never be undone by a retry of the recovered operation.
			removed, err := restarted.UpdateDispatchBindingsForOrg("org", host.ID, nanoflare.UpdateDispatchBindingsInput{DispatchNamespaces: []nanoflare.DispatchBinding{}, ExpectedDeploymentID: recovered.ID, IdempotencyKey: "remove-outbound"})
			must(err)
			repo = reopen()
			restarted, _ = managedService(t, repo, discardWriter{})
			replay, err = restarted.UpdateDispatchBindingsForOrg("org", host.ID, input)
			must(err)
			if replay.ID != recovered.ID {
				t.Fatal("lost durable outcome")
			}
			active, err = repo.ActiveDeploymentsForApp(host.ID)
			must(err)
			if len(active) != 1 || active[0].Deployment.ID != removed.ID {
				t.Fatal("historical retry reactivated stale bindings")
			}
			must(restarted.DeleteAppForOrg("org", policy.ID))
			replacement, err := restarted.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "policy", Hostname: "policy.test"})
			must(err)
			if replacement.ID == policy.ID {
				t.Fatal("replacement reused target identity")
			}
			_, err = restarted.Deploy(replacement.ID, code)
			must(err)
			repo = reopen()
			restarted, _ = managedService(t, repo, discardWriter{})
			retained, err := repo.GetDeploymentArtifact(host.ID, recovered.ID)
			must(err)
			if retained.Deployment.DispatchNamespaces[0].Outbound.WorkerID != policy.ID {
				t.Fatal("stored target rebound after recreation/reopen")
			}
			_, err = restarted.UpdateDispatchBindingsForOrg("org", host.ID, nanoflare.UpdateDispatchBindingsInput{DispatchNamespaces: bindings, ExpectedDeploymentID: removed.ID, IdempotencyKey: "must-not-rebind"})
			if err == nil {
				t.Fatal("deleted immutable target rebound by name")
			}
			writer := &outboundFailingWriter{fail: true}
			restarting, _ := managedService(t, repo, writer)
			bindings[0].Outbound.WorkerID = replacement.ID
			failed := nanoflare.UpdateDispatchBindingsInput{DispatchNamespaces: bindings, ExpectedDeploymentID: removed.ID, IdempotencyKey: "route-failure"}
			_, err = restarting.UpdateDispatchBindingsForOrg("org", host.ID, failed)
			if err == nil || writer.fail {
				t.Fatalf("route failure not reached: %v", err)
			}
			repo = reopen()
			restarted, _ = managedService(t, repo, discardWriter{})
			_, err = restarted.UpdateDispatchBindingsForOrg("org", host.ID, failed)
			if !errors.Is(err, nanoflare.ErrVarsRolloutFailed) {
				t.Fatalf("failed publication lost durable result: %v", err)
			}
			active, err = repo.ActiveDeploymentsForApp(host.ID)
			must(err)
			if len(active) != 1 || active[0].Deployment.ID != removed.ID || len(active[0].Deployment.DispatchNamespaces) != 0 {
				t.Fatal("failed route publication changed retained active revision")
			}
			restored, err := repo.GetDeploymentArtifact(host.ID, removed.ID)
			must(err)
			if !reflect.DeepEqual(restored.Secrets, original.Secrets) {
				t.Fatal("failed publication/restart changed frozen secretrefs")
			}

		})
	}
}
