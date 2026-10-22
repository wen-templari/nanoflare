package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type publicationFaultRepo struct {
	nanoflare.Repository
	failStage string
}

func (r *publicationFaultRepo) SavePublication(p nanoflare.Publication) error {
	if p.Stage == r.failStage {
		r.failStage = ""
		return errors.New("injected persistence interruption")
	}
	return r.Repository.SavePublication(p)
}

type stagedArtifactWriter struct {
	repo                    nanoflare.Repository
	t                       *testing.T
	failPrepare, failCommit bool
	prepared, committed     int
}

func (w *stagedArtifactWriter) Write([]nanoflare.ActiveDeployment) error { return nil }
func (w *stagedArtifactWriter) PreparePublication(active []nanoflare.ActiveDeployment) (nanoflare.PublicationPreparation, error) {
	w.prepared++
	for _, d := range active {
		if _, err := w.repo.GetDeploymentArtifact(d.App.ID, d.Deployment.ID); err != nil {
			w.t.Fatalf("prepare before durable artifact: %v", err)
		}
	}
	if w.failPrepare {
		w.failPrepare = false
		return nanoflare.PublicationPreparation{}, errors.New("unhealthy candidate")
	}
	return nanoflare.PublicationPreparation{Generation: "generation", Deployments: active}, nil
}
func (w *stagedArtifactWriter) CommitPublication(p nanoflare.PublicationPreparation) error {
	w.committed++
	for _, d := range p.Deployments {
		active, err := w.repo.ActiveDeploymentsForApp(d.App.ID)
		if err != nil {
			return err
		}
		found := false
		for _, a := range active {
			if a.Deployment.ID == d.Deployment.ID {
				found = true
			}
		}
		if !found {
			w.t.Fatal("commit preceded durable activation")
		}
	}
	if w.failCommit {
		w.failCommit = false
		return errors.New("route write failed")
	}
	return nil
}
func (*stagedArtifactWriter) AbortPublication(nanoflare.PublicationPreparation) error { return nil }

func TestImmutableArtifactsAndPublicationRecovery(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var repository nanoflare.Repository = nanoflare.NewStore()
			if backend == "postgres" {
				repository = runtimePostgres(t)
			}
			repo := &publicationFaultRepo{Repository: repository}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(repo.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org", UsageLevel: nanoflare.UsageLevelPaid}))
			writer := &stagedArtifactWriter{repo: repo, t: t}
			service := nanoflare.NewService(repo, writer)
			codec, err := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
			must(err)
			service.SetSecretCodec(codec)
			ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "production"})
			must(err)
			app, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha"})
			must(err)
			must(service.PutSecret(app.ID, "TOKEN", "old-secret-value"))
			input := nanoflare.DeployInput{IdempotencyKey: "first", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}, CompatibilityDate: "2025-12-10"}
			first, err := service.Deploy(app.ID, input)
			must(err)
			replay, err := service.Deploy(app.ID, input)
			must(err)
			if replay.ID != first.ID || writer.prepared != 1 {
				t.Fatal("retry duplicated publication")
			}
			manifest, err := service.ResolveNamespaceArtifactForOrg("org", ns.ID, "alpha", first.ID)
			must(err)
			if manifest.DeploymentID != first.ID || len(manifest.Secrets) != 1 || manifest.Policy.NamespaceID != ns.ID {
				t.Fatalf("manifest: %#v", manifest)
			}
			public, _ := json.Marshal(manifest)
			if strings.Contains(string(public), "old-secret-value") || strings.Contains(string(public), "ciphertext") || strings.Contains(string(public), "nonce") {
				t.Fatal("public manifest exposed sealed secrets")
			}
			if _, err = service.ArtifactForOrg("other", app.ID, first.ID); !errors.Is(err, nanoflare.ErrAppNotFound) {
				t.Fatalf("foreign artifact: %v", err)
			}
			original, err := repo.GetDeploymentArtifact(app.ID, first.ID)
			must(err)
			mutated := original
			mutated.Deployment.Files[0].Content = "mutated"
			if err = repo.PutDeploymentArtifact(mutated); !errors.Is(err, nanoflare.ErrArtifactConflict) {
				t.Fatalf("same digest accepted replacement: %v", err)
			}
			preserved, err := repo.GetDeploymentArtifact(app.ID, first.ID)
			must(err)
			if preserved.Deployment.Files[0].Content != "export default {}" {
				t.Fatal("read mutation changed stored source")
			}
			bad := input
			bad.Files = []nanoflare.WorkerFile{{Path: "worker.js", Content: "changed"}}
			if _, err = service.Deploy(app.ID, bad); !errors.Is(err, nanoflare.ErrIdempotencyConflict) {
				t.Fatalf("key conflict: %v", err)
			}
			// A failed runtime prepare leaves the prior revision selected.
			input.IdempotencyKey = "unhealthy"
			writer.failPrepare = true
			if _, err = service.Deploy(app.ID, input); err == nil {
				t.Fatal("unhealthy accepted")
			}
			active, err := service.ActiveDeployments()
			must(err)
			if len(active) != 1 || active[0].Deployment.ID != first.ID {
				t.Fatal("prepare failure changed traffic")
			}
			if _, err = service.Deploy(app.ID, input); !errors.Is(err, nanoflare.ErrPublicationFailed) {
				t.Fatalf("failed key retried: %v", err)
			}
			// Interrupt after runtime preparation; restart safely resumes the same revision.
			input.IdempotencyKey = "resume"
			repo.failStage = "activating"
			if _, err = service.Deploy(app.ID, input); err == nil {
				t.Fatal("injected interruption ignored")
			}
			pending, found, err := repo.GetPublication(app.ID, "resume")
			must(err)
			if !found || pending.Stage != "runtime_ready" {
				t.Fatalf("pending: %#v", pending)
			}
			service = nanoflare.NewService(repo, writer)
			service.SetSecretCodec(codec)
			must(service.RecoverPublications())
			resumed, err := service.Deploy(app.ID, input)
			must(err)
			if resumed.ID != pending.DeploymentID {
				t.Fatal("recovery duplicated revision")
			}

			// A stored older candidate cannot supersede a newer successful publication.
			input.IdempotencyKey = "stale-pending"
			repo.failStage = "activating"
			if _, err = service.Deploy(app.ID, input); err == nil {
				t.Fatal("interruption ignored")
			}
			stale, _, err := repo.GetPublication(app.ID, input.IdempotencyKey)
			must(err)
			input.IdempotencyKey = "newer-than-pending"
			newer, err := service.Deploy(app.ID, input)
			must(err)
			must(service.RecoverPublications())
			stale, _, err = repo.GetPublication(app.ID, "stale-pending")
			must(err)
			if stale.Stage != "superseded" {
				t.Fatalf("stale pending stage: %s", stale.Stage)
			}
			active, err = service.ActiveDeployments()
			must(err)
			if active[0].Deployment.ID != newer.ID {
				t.Fatal("recovery reactivated stale candidate")
			}
			// Lost final acknowledgement is reconciled from durable activation.
			input.IdempotencyKey = "ack"
			repo.failStage = "route_published"
			if _, err = service.Deploy(app.ID, input); err == nil {
				t.Fatal("lost acknowledgement not surfaced")
			}
			must(service.RecoverPublications())
			status, err := service.PublicationForOrg("org", app.ID, "ack")
			must(err)
			if status.Stage != "route_published" || !status.RuntimeReady {
				t.Fatalf("status: %#v", status)
			}
			// An older successful key never changes a later selection.
			_, err = service.Deploy(app.ID, nanoflare.DeployInput{IdempotencyKey: "first", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}, CompatibilityDate: "2025-12-10"})
			must(err)
			active, err = service.ActiveDeployments()
			must(err)
			if active[0].Deployment.ID != status.DeploymentID {
				t.Fatal("replay reactivated old revision")
			}
			// Secret publication preserves weighted traffic and rollback pins old secrets.
			must(repo.SetActiveTraffic(app.ID, []nanoflare.DeploymentTraffic{{ID: first.ID, TrafficPercent: 40}, {ID: status.DeploymentID, TrafficPercent: 60}}))
			must(service.PutSecret(app.ID, "TOKEN", "new-secret-value"))
			active, err = service.ActiveDeployments()
			must(err)
			weights := map[int]bool{}
			for _, d := range active {
				weights[d.TrafficPercent] = true
				if d.App.SecretValues["TOKEN"] != "new-secret-value" {
					t.Fatal("new secret missing")
				}
			}
			if len(active) != 2 || !weights[40] || !weights[60] {
				t.Fatalf("secret rollout collapsed traffic: %#v", active)
			}
			must(repo.SetActive(app.ID, first.ID))
			active, err = service.ActiveDeployments()
			must(err)
			if active[0].App.SecretValues["TOKEN"] != "old-secret-value" {
				t.Fatal("rollback mixed new secrets with old source")
			}
			// A failed commit durably restores the previous traffic and secret snapshot.
			input.IdempotencyKey = "commit-failure"
			writer.failCommit = true
			if _, err = service.Deploy(app.ID, input); err == nil {
				t.Fatal("commit failure ignored")
			}
			active, err = service.ActiveDeployments()
			must(err)
			if active[0].Deployment.ID != first.ID || active[0].App.SecretValues["TOKEN"] != "old-secret-value" {
				t.Fatal("commit rollback changed prior snapshot")
			}
			// Persisted rollback intent survives an interrupted rollback acknowledgement.
			input.IdempotencyKey = "rollback-recovery"
			writer.failCommit = true
			repo.failStage = "failed"
			if _, err = service.Deploy(app.ID, input); err == nil {
				t.Fatal("interrupted rollback accepted")
			}
			pending, _, err = repo.GetPublication(app.ID, input.IdempotencyKey)
			must(err)
			if pending.Stage != "rolling_back" {
				t.Fatalf("rollback intent: %s", pending.Stage)
			}
			must(service.RecoverPublications())
			pending, _, err = repo.GetPublication(app.ID, input.IdempotencyKey)
			must(err)
			if pending.Stage != "failed" {
				t.Fatalf("recovered rollback: %s", pending.Stage)
			}
			// Independent service instances share the repository publication guard.
			before, err := repo.ListDeployments()
			must(err)
			concurrent := input
			concurrent.IdempotencyKey = "concurrent"
			type outcome struct {
				deployment nanoflare.Deployment
				err        error
			}
			results := make(chan outcome, 6)
			var wg sync.WaitGroup
			for i := 0; i < 6; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					instance := nanoflare.NewService(repo, writer)
					instance.SetSecretCodec(codec)
					d, e := instance.Deploy(app.ID, concurrent)
					results <- outcome{d, e}
				}()
			}
			wg.Wait()
			close(results)
			selected := ""
			for result := range results {
				must(result.err)
				if selected == "" {
					selected = result.deployment.ID
				}
				if selected != result.deployment.ID {
					t.Fatal("concurrent retries created different revisions")
				}
			}
			after, err := repo.ListDeployments()
			must(err)
			if len(after) != len(before)+1 {
				t.Fatal("concurrent retries duplicated persistent revisions")
			}

		})
	}
}

func TestArtifactAPIProtectionAndIdempotency(t *testing.T) {
	repo := nanoflare.NewStore()
	if err := repo.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org", UsageLevel: nanoflare.UsageLevelPaid}); err != nil {
		t.Fatal(err)
	}
	service := nanoflare.NewService(repo, discardWriter{})
	ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "production"})
	if err != nil {
		t.Fatal(err)
	}
	app, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(service)
	server.controlAuth = nanoflare.NewControlAuthService(repo, "fixture")
	request := func(method, path, body, scope, org, key string, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, "/v1/organizations/"+org+path, strings.NewReader(body))
		r.Header.Set("Idempotency-Key", key)
		ctx := context.WithValue(r.Context(), controlOrgKey, org)
		ctx = context.WithValue(ctx, controlScopesKey, strings.Fields(scope))
		w := httptest.NewRecorder()
		server.mux.ServeHTTP(w, r.WithContext(ctx))
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	base := "/workers/" + app.ID
	body := `{"files":[{"path":"worker.js","content":"export default {}"}],"compatibility_date":"2025-12-10"}`
	first := request("POST", base+"/deployments", body, "deployments:write", "org", "http-key", 201)
	second := request("POST", base+"/deployments", body, "deployments:write", "org", "http-key", 201)
	if string(first) != string(second) {
		t.Fatal("HTTP idempotency returned a different revision")
	}
	var deployment nanoflare.Deployment
	if err = json.Unmarshal(first, &deployment); err != nil {
		t.Fatal(err)
	}
	artifactPath := base + "/deployments/" + deployment.ID + "/artifact"
	request("GET", artifactPath, "", "deployments:write", "org", "", 403)
	request("GET", artifactPath, "", "workers:read", "other", "", 404)
	request("GET", artifactPath, "", "workers:read", "org", "", 200)
	request("GET", base+"/publications/http-key", "", "workers:read", "org", "", 200)
	request("GET", base+"/publications/unknown", "", "workers:read", "org", "", 404)
	request("GET", "/dispatch-namespaces/"+ns.ID+"/scripts/alpha/artifact", "", "workers:read", "org", "", 403)
	request("GET", "/dispatch-namespaces/"+ns.ID+"/scripts/alpha/artifact", "", "namespaces:dispatch", "org", "", 200)
	request("POST", base+"/deployments", strings.Replace(body, "export default {}", "export default {x:1}", 1), "deployments:write", "org", "http-key", 409)
	request("POST", base+"/deployments", `{"idempotency_key":"other"}`, "deployments:write", "org", "http-key", 400)
}
