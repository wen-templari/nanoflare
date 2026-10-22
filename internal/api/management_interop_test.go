package api

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestManagedAndNativeMutationInteroperability(t *testing.T) {
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
			service, codec := managedService(t, repo, discardWriter{})
			ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "interop"})
			must(err)
			app, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
			must(err)
			// Legacy saved records must not reappear after a complete managed upload.
			must(service.PutSecret(app.ID, "LEGACY", "do-not-resurrect"))
			input := nanoflare.ManagedDeployInput{Deployment: nanoflare.DeployInput{IdempotencyKey: "v1", Format: "modules", CompatibilityDate: "2025-12-10", Vars: map[string]json.RawMessage{"MODE": json.RawMessage(`"one"`)}, Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {version:'v1'}"}}}, Metadata: nanoflare.ScriptManagementMetadata{Tags: []string{"customer-a"}, Bindings: []json.RawMessage{json.RawMessage(`{"name":"MODE","type":"json","json":"one"}`), json.RawMessage(`{"name":"TOKEN","type":"secret_text"}`)}}, Secrets: map[string]string{"TOKEN": "token-v1"}}
			first, err := service.ManagedDeployForOrg("org", app.ID, input)
			must(err)
			originalListing, err := service.ListSecretsForOrg("org", app.ID)
			must(err)
			if len(originalListing) != 1 {
				t.Fatal("managed listing included stale legacy secret")
			}
			input.ExpectedRevision = 1
			input.BaseDeploymentID = first.ID
			input.Deployment.IdempotencyKey = "v2"
			input.Deployment.Files[0].Content = "export default {version:'v2'}"
			input.Secrets = map[string]string{"TOKEN": "token-v2"}
			second, err := service.ManagedDeployForOrg("org", app.ID, input)
			must(err)
			native := input.Deployment
			native.IdempotencyKey = "native-v3"
			native.Files = []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {version:'native-v3'}"}}
			native.Vars = map[string]json.RawMessage{"NEW": json.RawMessage(`42`)}
			third, err := service.DeployForOrg("org", app.ID, native)
			must(err)
			state, _, err := service.ScriptManagementForOrg("org", app.ID)
			must(err)
			if state.Revision != 3 || state.DeploymentID != third.ID || state.Metadata.Tags[0] != "customer-a" {
				t.Fatal("native deploy did not advance adapter metadata")
			}
			assertSecret := func(revision, name, want string) {
				t.Helper()
				artifact, err := repo.GetDeploymentArtifact(app.ID, revision)
				must(err)
				if len(artifact.Secrets) != 1 || artifact.Secrets[0].Name != name {
					t.Fatal("wrong complete secret set", artifact.Secrets)
				}
				value, err := codec.Decrypt(artifact.Secrets[0].Nonce, artifact.Secrets[0].Ciphertext)
				must(err)
				if value != want {
					t.Fatal("wrong secret value")
				}
			}
			assertSecret(third.ID, "TOKEN", "token-v2")
			_, err = service.SetDeploymentTrafficForOrg("org", app.ID, []nanoflare.DeploymentTraffic{{ID: first.ID, TrafficPercent: 25}, {ID: second.ID, TrafficPercent: 75}})
			must(err)
			must(service.PutSecretForOrg("org", app.ID, "TOKEN", "native-updated"))
			state, _, err = service.ScriptManagementForOrg("org", app.ID)
			must(err)
			if state.Revision != 4 || state.DeploymentID == third.ID {
				t.Fatal("native secret update lost latest upload")
			}
			latest, err := repo.GetDeploymentArtifact(app.ID, state.DeploymentID)
			must(err)
			if !strings.Contains(latest.Deployment.Files[0].Content, "native-v3") {
				t.Fatal("secret edit replaced latest code with a serving rollback")
			}
			assertSecret(state.DeploymentID, "TOKEN", "native-updated")
			active, err := repo.ActiveDeploymentsForApp(app.ID)
			must(err)
			weights := map[int]bool{}
			for _, item := range active {
				weights[item.TrafficPercent] = true
				assertSecret(item.Deployment.ID, "TOKEN", "native-updated")
				if strings.Contains(item.Deployment.Files[0].Content, "native-v3") {
					t.Fatal("native secret edit collapsed split traffic to latest code")
				}
			}
			if len(active) != 2 || !weights[25] || !weights[75] {
				t.Fatal("native secret edit changed traffic weights")
			}
			listed, err := service.ListSecretsForOrg("org", app.ID)
			must(err)
			if len(listed) != 1 || listed[0].Name != "TOKEN" {
				t.Fatal("secret listing read stale legacy records")
			}
			if !listed[0].CreatedAt.Equal(originalListing[0].CreatedAt) || !listed[0].UpdatedAt.After(originalListing[0].UpdatedAt) {
				t.Fatal("native secret mutation lost creation/update timestamps")
			}
			must(service.DeleteSecretForOrg("org", app.ID, "TOKEN"))
			listed, err = service.ListSecretsForOrg("org", app.ID)
			must(err)
			if len(listed) != 0 {
				t.Fatal("deleted managed secret remains visible")
			}
			active, err = repo.ActiveDeploymentsForApp(app.ID)
			must(err)
			for _, item := range active {
				a, err := repo.GetDeploymentArtifact(app.ID, item.Deployment.ID)
				must(err)
				if len(a.Secrets) != 0 {
					t.Fatal("deleted secret retained in newly active artifacts")
				}
			}
			// A native vars edit deliberately derives from the explicit serving base,
			// including its pinned secret set, and becomes a new latest upload.
			_, err = service.SetDeploymentTrafficForOrg("org", app.ID, []nanoflare.DeploymentTraffic{{ID: first.ID, TrafficPercent: 100}})
			must(err)
			vars, err := service.UpdateVarsForOrg("org", app.ID, nanoflare.UpdateVarsInput{ExpectedDeploymentID: first.ID, IdempotencyKey: "native-vars", Vars: map[string]json.RawMessage{"MODE": json.RawMessage(`"changed"`)}})
			must(err)
			state, _, err = service.ScriptManagementForOrg("org", app.ID)
			must(err)
			if state.Revision != 6 || state.DeploymentID != vars.ID {
				t.Fatal("vars edit did not update latest upload")
			}
			assertSecret(vars.ID, "TOKEN", "token-v1")
			var mode struct {
				Type string          `json:"type"`
				JSON json.RawMessage `json:"json"`
			}
			for _, raw := range state.Metadata.Bindings {
				var name struct {
					Name string `json:"name"`
				}
				must(json.Unmarshal(raw, &name))
				if name.Name == "MODE" {
					must(json.Unmarshal(raw, &mode))
				}
			}
			// The intervening native deploy removed MODE, so the explicit native vars
			// request introduces a string variable using native plain-text semantics.
			if mode.Type != "plain_text" {
				t.Fatal("vars metadata was not rebuilt", mode.Type)
			}
			if _, err = service.UpdateVarsForOrg("org", app.ID, nanoflare.UpdateVarsInput{ExpectedDeploymentID: first.ID, IdempotencyKey: "native-vars", Vars: map[string]json.RawMessage{"MODE": json.RawMessage(`"changed"`)}}); err != nil {
				t.Fatal("vars retry failed", err)
			}
			state, _, err = service.ScriptManagementForOrg("org", app.ID)
			must(err)
			if state.Revision != 6 {
				t.Fatal("vars retry advanced metadata twice")
			}
			if err = service.DeleteSecretForOrg("org", app.ID, "missing"); !errors.Is(err, nanoflare.ErrSecretNotFound) {
				t.Fatal("missing secret deletion accepted", err)
			}
		})
	}
}

func TestManagedNativeVarsMetadataRecoversAfterCommitFailure(t *testing.T) {
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
			repo := &managedCommitFailure{Repository: base}
			service, _ := managedService(t, repo, discardWriter{})
			ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "vars-recover"})
			must(err)
			app, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
			must(err)
			first, err := service.ManagedDeployForOrg("org", app.ID, nanoflare.ManagedDeployInput{Deployment: nanoflare.DeployInput{IdempotencyKey: "first", Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}, Vars: map[string]json.RawMessage{"TEXT": json.RawMessage(`"one"`)}}, Metadata: nanoflare.ScriptManagementMetadata{Bindings: []json.RawMessage{json.RawMessage(`{"name":"TEXT","type":"json","json":"one"}`)}}})
			must(err)
			repo.fail = true
			input := nanoflare.UpdateVarsInput{ExpectedDeploymentID: first.ID, IdempotencyKey: "vars", Vars: map[string]json.RawMessage{"MODE": json.RawMessage(`null`), "TEXT": json.RawMessage(`"changed"`), "BIG": json.RawMessage(`9007199254740993`)}}
			if _, err := service.UpdateVarsForOrg("org", app.ID, input); err == nil {
				t.Fatal("metadata commit failure ignored")
			}
			restarted, _ := managedService(t, base, discardWriter{})
			must(restarted.RecoverPublications())
			result, err := restarted.UpdateVarsForOrg("org", app.ID, input)
			must(err)
			state, _, err := restarted.ScriptManagementForOrg("org", app.ID)
			must(err)
			if state.Revision != 2 || state.DeploymentID != result.ID {
				t.Fatal("vars metadata failed restart reconciliation")
			}
			projected := map[string]json.RawMessage{}
			for _, raw := range state.Metadata.Bindings {
				var binding struct {
					Name string          `json:"name"`
					Type string          `json:"type"`
					JSON json.RawMessage `json:"json"`
				}
				must(json.Unmarshal(raw, &binding))
				if binding.Type != "json" {
					t.Fatal("native vars lost JSON binding type", binding.Name)
				}
				projected[binding.Name] = binding.JSON
			}
			if string(projected["MODE"]) != "null" || string(projected["TEXT"]) != `"changed"` || string(projected["BIG"]) != "9007199254740993" {
				t.Fatal("native metadata changed null/string/number values", projected)
			}
		})
	}
}

func TestManagedNativeSecretEditDoesNotActivateInactiveCode(t *testing.T) {
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
			ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "inactive"})
			must(err)
			app, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
			must(err)
			writer.failNext = true
			_, err = service.ManagedDeployForOrg("org", app.ID, nanoflare.ManagedDeployInput{Deployment: nanoflare.DeployInput{IdempotencyKey: "failed-first", Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}}, Secrets: map[string]string{"TOKEN": "old"}, Metadata: nanoflare.ScriptManagementMetadata{Bindings: []json.RawMessage{json.RawMessage(`{"name":"TOKEN","type":"secret_text"}`)}}})
			if err == nil {
				t.Fatal("activation failure ignored")
			}
			before := len(writer.writes)
			must(service.PutSecret(app.ID, "TOKEN", "new"))
			if len(writer.writes) != before {
				t.Fatal("inactive secret edit published runtime configuration")
			}
			active, err := repo.ActiveDeploymentsForApp(app.ID)
			must(err)
			if len(active) != 0 {
				t.Fatal("inactive secret edit activated code")
			}
			state, _, err := service.ScriptManagementForOrg("org", app.ID)
			must(err)
			if state.Revision != 2 {
				t.Fatal("inactive secret edit was not stored")
			}
			artifact, err := repo.GetDeploymentArtifact(app.ID, state.DeploymentID)
			must(err)
			value, err := codec.Decrypt(artifact.Secrets[0].Nonce, artifact.Secrets[0].Ciphertext)
			must(err)
			if value != "new" {
				t.Fatal("inactive secret value was not updated")
			}
			restarted, _ := managedService(t, repo, writer)
			must(restarted.RecoverPublications())
			active, err = repo.ActiveDeploymentsForApp(app.ID)
			must(err)
			if len(active) != 0 {
				t.Fatal("restart activated configuration-only secret edit")
			}
		})
	}
}
