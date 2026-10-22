package api

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type managedRecordedRevision struct{ id, code, secret string }

type managedCommitFailure struct {
	nanoflare.Repository
	commitThenFail bool
	fail           bool
}

func (r *managedCommitFailure) CommitScriptManagementState(state nanoflare.ScriptManagementState, expected int64) error {
	if r.fail {
		r.fail = false
		if r.commitThenFail {
			if err := r.Repository.CommitScriptManagementState(state, expected); err != nil {
				return err
			}
		}
		return errors.New("controlled management commit failure")
	}
	return r.Repository.CommitScriptManagementState(state, expected)
}

type managedRecordingWriter struct {
	mu       sync.Mutex
	failNext bool
	writes   [][]managedRecordedRevision
}

func (w *managedRecordingWriter) Write(active []nanoflare.ActiveDeployment) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var snapshot []managedRecordedRevision
	for _, item := range active {
		code := ""
		if len(item.Deployment.Files) > 0 {
			code = item.Deployment.Files[0].Content
		}
		snapshot = append(snapshot, managedRecordedRevision{item.Deployment.ID, code, item.App.SecretValues["TOKEN"]})
	}
	w.writes = append(w.writes, snapshot)
	if w.failNext {
		w.failNext = false
		return errors.New("controlled publication failure")
	}
	return nil
}
func managedService(t *testing.T, repo nanoflare.Repository, writer nanoflare.ConfigWriter) (*nanoflare.Service, *nanoflare.SecretCodec) {
	t.Helper()
	service := nanoflare.NewService(repo, writer)
	service.SetPlatformDispatchEnabled(true)
	codec, err := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	service.SetSecretCodec(codec)
	return service, codec
}
func TestManagedPublicationAtomicSecretsLatestUploadAndRecovery(t *testing.T) {
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
			writer := &managedRecordingWriter{}
			service, codec := managedService(t, repo, writer)
			ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "managed"})
			must(err)
			app, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
			must(err)
			input := nanoflare.ManagedDeployInput{Deployment: nanoflare.DeployInput{IdempotencyKey: "managed-v1", Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {version:'v1'}"}}}, Metadata: nanoflare.ScriptManagementMetadata{Bindings: []json.RawMessage{json.RawMessage(`{"name":"TOKEN","type":"secret_text"}`)}, Tags: []string{"customer-a"}}, Secrets: map[string]string{"TOKEN": "secret-v1"}}
			first, err := service.ManagedDeployForOrg("org", app.ID, input)
			must(err)
			state, found, err := service.ScriptManagementForOrg("org", app.ID)
			must(err)
			if !found || state.Revision != 1 || state.DeploymentID != first.ID {
				t.Fatal("management pointer missing")
			}
			if _, _, err = service.ScriptManagementForOrg("foreign", app.ID); !errors.Is(err, nanoflare.ErrAppNotFound) {
				t.Fatal("cross-org metadata accepted")
			}
			input.Deployment.IdempotencyKey = "managed-v2"
			input.Deployment.Files[0].Content = "export default {version:'v2'}"
			input.ExpectedRevision = 1
			input.BaseDeploymentID = first.ID
			input.Secrets = map[string]string{"TOKEN": "secret-v2"}
			input.Metadata.Tags = []string{"new-tag"}
			second, err := service.ManagedDeployForOrg("org", app.ID, input)
			must(err)
			artifact, err := repo.GetDeploymentArtifact(app.ID, second.ID)
			must(err)
			if artifact.Management == nil || artifact.Management.Metadata.Tags[0] != "new-tag" || len(artifact.Secrets) != 1 {
				t.Fatal("atomic artifact metadata missing")
			}
			value, err := codec.Decrypt(artifact.Secrets[0].Nonce, artifact.Secrets[0].Ciphertext)
			must(err)
			if value != "secret-v2" {
				t.Fatal("wrong pinned secret")
			}
			encoded, err := json.Marshal(artifact)
			must(err)
			if strings.Contains(string(encoded), "secret-v2") {
				t.Fatal("plaintext secret persisted")
			}
			storedSecrets, err := repo.ListSecrets(app.ID)
			must(err)
			if len(storedSecrets) != 0 {
				t.Fatal("managed publication edited legacy live secret store")
			}
			_, err = service.SetDeploymentTrafficForOrg("org", app.ID, []nanoflare.DeploymentTraffic{{ID: first.ID, TrafficPercent: 100}})
			must(err)
			state, found, err = service.ScriptManagementForOrg("org", app.ID)
			must(err)
			if !found || state.Revision != 2 || state.DeploymentID != second.ID {
				t.Fatal("rollback rewound latest upload")
			}
			// A new service instance reads persisted state and retries without moving
			// traffic back to the successfully published revision.
			restarted, _ := managedService(t, repo, writer)
			retry, err := restarted.ManagedDeployForOrg("org", app.ID, input)
			must(err)
			if retry.ID != second.ID {
				t.Fatal("retry created a revision")
			}
			active, err := repo.ActiveDeploymentsForApp(app.ID)
			must(err)
			if len(active) != 1 || active[0].Deployment.ID != first.ID {
				t.Fatal("retry overwrote rollback")
			}
			changed := input
			changed.Secrets = map[string]string{"TOKEN": "changed"}
			if _, err = restarted.ManagedDeployForOrg("org", app.ID, changed); !errors.Is(err, nanoflare.ErrIdempotencyConflict) {
				t.Fatal("changed secret reused idempotency key", err)
			}
			stale := input
			stale.Deployment.IdempotencyKey = "stale"
			if _, err = restarted.ManagedDeployForOrg("org", app.ID, stale); !errors.Is(err, nanoflare.ErrManagementConflict) {
				t.Fatal("stale update accepted", err)
			}
			// Publication failure restores old traffic and keeps the fully stored
			// upload as latest; no old-code/new-secret intermediate revision exists.
			input.Deployment.IdempotencyKey = "managed-v3-fails"
			input.ExpectedRevision = 2
			input.BaseDeploymentID = second.ID
			input.Deployment.Files[0].Content = "export default {version:'v3'}"
			input.Secrets = map[string]string{"TOKEN": "secret-v3"}
			writer.failNext = true
			if _, err = restarted.ManagedDeployForOrg("org", app.ID, input); err == nil {
				t.Fatal("controlled failure not observed")
			}
			active, err = repo.ActiveDeploymentsForApp(app.ID)
			must(err)
			if len(active) != 1 || active[0].Deployment.ID != first.ID {
				t.Fatal("failed activation did not restore traffic")
			}
			state, _, err = restarted.ScriptManagementForOrg("org", app.ID)
			must(err)
			if state.Revision != 3 || state.DeploymentID == second.ID {
				t.Fatal("stored latest upload lost on activation failure")
			}
			for _, write := range writer.writes {
				for _, item := range write {
					switch {
					case strings.Contains(item.code, "'v1'"):
						if item.secret != "secret-v1" {
							t.Fatal("mixed v1 code/secret")
						}
					case strings.Contains(item.code, "'v2'"):
						if item.secret != "secret-v2" {
							t.Fatal("mixed v2 code/secret")
						}
					case strings.Contains(item.code, "'v3'"):
						if item.secret != "secret-v3" {
							t.Fatal("mixed v3 code/secret")
						}
					}
				}
			}
			// Metadata is copied and secrets may be inherited/renamed from the latest
			// uploaded artifact even though traffic remains on the older revision.
			state.Metadata.Tags[0] = "tampered"
			again, _, err := restarted.ScriptManagementForOrg("org", app.ID)
			must(err)
			if again.Metadata.Tags[0] == "tampered" {
				t.Fatal("mutable metadata state")
			}
			input.Deployment.IdempotencyKey = "renamed"
			input.ExpectedRevision = 3
			input.BaseDeploymentID = again.DeploymentID
			input.Secrets = nil
			input.KeepSecrets = map[string]string{"RENAMED": "TOKEN"}
			input.Metadata.Bindings = []json.RawMessage{json.RawMessage(`{"name":"RENAMED","type":"secret_text"}`)}
			renamed, err := restarted.ManagedDeployForOrg("org", app.ID, input)
			must(err)
			artifact, err = repo.GetDeploymentArtifact(app.ID, renamed.ID)
			must(err)
			if len(artifact.Secrets) != 1 || artifact.Secrets[0].Name != "RENAMED" {
				t.Fatal("secret rename failed")
			}
			value, err = codec.Decrypt(artifact.Secrets[0].Nonce, artifact.Secrets[0].Ciphertext)
			must(err)
			if value != "secret-v3" {
				t.Fatal("inheritance used serving revision instead of latest upload")
			}
		})
	}
}

func TestManagedPublicationConcurrentWritersAndSecretMetadataRejection(t *testing.T) {
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
			one, _ := managedService(t, repo, discardWriter{})
			two, _ := managedService(t, repo, discardWriter{})
			ns, err := one.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "concurrent"})
			must(err)
			app, err := one.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
			must(err)
			input := nanoflare.ManagedDeployInput{Deployment: nanoflare.DeployInput{IdempotencyKey: "a", Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}}}
			bad := input
			bad.Metadata.Bindings = []json.RawMessage{json.RawMessage(`{"type":"secret_text","name":"TOKEN","text":"do-not-store"}`)}
			if _, err := one.ManagedDeployForOrg("org", app.ID, bad); err == nil {
				t.Fatal("plaintext secret metadata accepted")
			}
			results := make(chan error, 2)
			start := make(chan struct{})
			for index, service := range []*nanoflare.Service{one, two} {
				go func(index int, service *nanoflare.Service) {
					<-start
					copy := input
					if index == 1 {
						copy.Deployment.IdempotencyKey = "b"
					}
					_, err := service.ManagedDeployForOrg("org", app.ID, copy)
					results <- err
				}(index, service)
			}
			close(start)
			success, conflict := 0, 0
			for range 2 {
				err := <-results
				if err == nil {
					success++
				} else if errors.Is(err, nanoflare.ErrManagementConflict) {
					conflict++
				} else {
					t.Fatal(err)
				}
			}
			if success != 1 || conflict != 1 {
				t.Fatalf("success=%d conflict=%d", success, conflict)
			}
		})
	}
}

func TestManagedPublicationRecoversMetadataCommitAndUnknownOutcome(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		for _, unknown := range []bool{false, true} {
			name := backend + "/before-commit"
			if unknown {
				name = backend + "/unknown-outcome"
			}
			t.Run(name, func(t *testing.T) {
				var base nanoflare.Repository = nanoflare.NewStore()
				if backend == "postgres" {
					base = runtimePostgres(t)
				}
				must := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				must(base.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org", UsageLevel: nanoflare.UsageLevelPaid}))
				repo := &managedCommitFailure{Repository: base, fail: true, commitThenFail: unknown}
				writer := &managedRecordingWriter{}
				service, _ := managedService(t, repo, writer)
				ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "recover"})
				must(err)
				app, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
				must(err)
				input := nanoflare.ManagedDeployInput{Deployment: nanoflare.DeployInput{IdempotencyKey: "recover", Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}}}
				if _, err = service.ManagedDeployForOrg("org", app.ID, input); err == nil {
					t.Fatal("commit failure was ignored")
				}
				publication, found, err := base.GetPublication(app.ID, "recover")
				must(err)
				if !found || publication.Stage != "stored" {
					t.Fatal("durable retry intent missing")
				}
				active, err := base.ActiveDeploymentsForApp(app.ID)
				must(err)
				if len(active) != 0 {
					t.Fatal("metadata failure activated code")
				}
				restarted, _ := managedService(t, base, writer)
				must(restarted.RecoverPublications())
				deployment, err := restarted.ManagedDeployForOrg("org", app.ID, input)
				must(err)
				if deployment.ID != publication.DeploymentID {
					t.Fatal("recovery created another artifact")
				}
				state, found, err := restarted.ScriptManagementForOrg("org", app.ID)
				must(err)
				if !found || state.Revision != 1 || state.DeploymentID != deployment.ID {
					t.Fatal("recovery failed to reconcile metadata")
				}
				must(restarted.DeleteAppForOrg("org", app.ID))
				if _, _, err = base.GetScriptManagementState(app.ID); !errors.Is(err, nanoflare.ErrAppNotFound) {
					t.Fatal("deleted Worker retained management metadata", err)
				}
			})
		}
	}
}

func TestManagedRecoverySupersedesOlderPendingUpload(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var base nanoflare.Repository = nanoflare.NewStore()
			if backend == "postgres" {
				base = runtimePostgres(t)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(base.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org", UsageLevel: nanoflare.UsageLevelPaid}))
			repo := &managedCommitFailure{Repository: base, fail: true}
			service, _ := managedService(t, repo, discardWriter{})
			ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "superseded"})
			must(err)
			app, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
			must(err)
			input := nanoflare.ManagedDeployInput{Deployment: nanoflare.DeployInput{IdempotencyKey: "old-pending", Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}}}
			if _, err = service.ManagedDeployForOrg("org", app.ID, input); err == nil {
				t.Fatal("controlled failure ignored")
			}
			input.Deployment.IdempotencyKey = "newer"
			newer, err := service.ManagedDeployForOrg("org", app.ID, input)
			must(err)
			restarted, _ := managedService(t, base, discardWriter{})
			must(restarted.RecoverPublications())
			old, _, err := base.GetPublication(app.ID, "old-pending")
			must(err)
			if old.Stage != "superseded" {
				t.Fatal("stale pending upload blocks recovery", old.Stage)
			}
			active, err := base.ActiveDeploymentsForApp(app.ID)
			must(err)
			if len(active) != 1 || active[0].Deployment.ID != newer.ID {
				t.Fatal("recovery replaced newer traffic")
			}
		})
	}
}
