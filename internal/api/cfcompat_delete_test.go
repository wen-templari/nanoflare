package api

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type uncertainNamespaceDeleteRepo struct {
	nanoflare.Repository
	failAfterDelete bool
}

func (r *uncertainNamespaceDeleteRepo) DeleteNamespaceScripts(org, ns string, ids []string) error {
	if err := r.Repository.DeleteNamespaceScripts(org, ns, ids); err != nil {
		return err
	}
	if r.failAfterDelete {
		r.failAfterDelete = false
		return errors.New("controlled unknown transaction outcome")
	}
	return nil
}

func TestCloudflareTagDeletionAtomicScopeAndReconciliation(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var baseRepo nanoflare.Repository = nanoflare.NewStore()
			if backend == "postgres" {
				baseRepo = runtimePostgres(t)
			}
			repo := &uncertainNamespaceDeleteRepo{Repository: baseRepo}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			auth := nanoflare.NewControlAuthService(repo, "tag-delete")
			auth.SetTestHashCost(4)
			session, err := auth.Signup(nanoflare.SignupInput{Email: "delete@example.com", Password: "password"})
			must(err)
			org := nanoflare.Organization{ID: "delete-org", Name: "delete", UsageLevel: nanoflare.UsageLevelPaid}
			otherOrg := nanoflare.Organization{ID: "other-org", Name: "other", UsageLevel: nanoflare.UsageLevelPaid}
			for _, item := range []nanoflare.Organization{org, otherOrg} {
				must(repo.CreateOrganization(item))
				must(repo.UpsertOrganizationMembership(nanoflare.OrganizationMembership{UserID: session.User.ID, OrgID: item.ID, Role: nanoflare.RoleOwner, Scopes: nanoflare.RoleScopes(nanoflare.RoleOwner)}))
			}
			writer := &managedRecordingWriter{}
			service, _ := managedService(t, repo, writer)
			ns, err := service.CreateDispatchNamespace(org.ID, nanoflare.CreateDispatchNamespaceInput{Name: "staging", CustomerMode: "platform"})
			must(err)
			otherNS, err := service.CreateDispatchNamespace(org.ID, nanoflare.CreateDispatchNamespaceInput{Name: "other"})
			must(err)
			foreignNS, err := service.CreateDispatchNamespace(otherOrg.ID, nanoflare.CreateDispatchNamespaceInput{Name: "staging"})
			must(err)
			create := func(owner, namespace, name, customer, tag string) (nanoflare.App, nanoflare.Deployment) {
				t.Helper()
				app, err := service.CreateNamespaceScript(owner, namespace, nanoflare.CreateNamespaceScriptInput{ScriptName: name, CustomerID: customer, Execution: "dynamic"})
				must(err)
				d, err := service.ManagedDeployForOrg(owner, app.ID, nanoflare.ManagedDeployInput{Deployment: nanoflare.DeployInput{IdempotencyKey: "first", Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}}, Metadata: nanoflare.ScriptManagementMetadata{Bindings: []json.RawMessage{}, Tags: []string{tag}}})
				must(err)
				return app, d
			}
			a, aFirst := create(org.ID, ns.ID, "a", "alice", "keep")
			_, err = service.ManagedDeployForOrg(org.ID, a.ID, nanoflare.ManagedDeployInput{ExpectedRevision: 1, BaseDeploymentID: aFirst.ID, Deployment: nanoflare.DeployInput{IdempotencyKey: "second", Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {version:2}"}}}, Metadata: nanoflare.ScriptManagementMetadata{Bindings: []json.RawMessage{}, Tags: []string{"delete"}}})
			must(err)
			_, err = service.SetDeploymentTrafficForOrg(org.ID, a.ID, []nanoflare.DeploymentTraffic{{ID: aFirst.ID, TrafficPercent: 100}})
			must(err)
			b, bFirst := create(org.ID, ns.ID, "b", "bob", "delete")
			keep, _ := create(org.ID, ns.ID, "keep", "alice", "keep")
			other, _ := create(org.ID, otherNS.ID, "a", "", "delete")
			foreign, _ := create(otherOrg.ID, foreignNS.ID, "a", "", "delete")
			platform, err := service.CreateNamespaceScript(org.ID, ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "platform", Kind: "platform", Execution: "static"})
			must(err)
			kv, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: org.ID, Name: "retained-resource"})
			must(err)
			for _, ids := range [][]string{{a.ID, other.ID}, {a.ID, foreign.ID}, {a.ID, a.ID}} {
				if err = repo.DeleteNamespaceScripts(org.ID, ns.ID, ids); !errors.Is(err, nanoflare.ErrNamespaceDeletionConflict) {
					t.Fatalf("unsafe repository selection: %v", err)
				}
				_, err = repo.GetApp(a.ID)
				must(err)
			}
			handler := NewServerWithControlAuth(service, nil, "", nil, auth)
			handler.SetCloudflareCompatibilityEnabled(true)
			base := "/client/v4/accounts/" + org.ID + "/workers/dispatch/namespaces/staging/scripts"
			read, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "read", ScopeType: "org", OrgID: org.ID, Scopes: []string{"workers:read"}})
			must(err)
			write, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "write", ScopeType: "org", OrgID: org.ID, Scopes: []string{"workers:write"}})
			must(err)
			call := func(method, path, token, header string, want int) *httptest.ResponseRecorder {
				t.Helper()
				r := httptest.NewRequest(method, path, nil)
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("X-Nanoflare-Execution-Profile", "trusted_shared_workerd")
				if header != "" {
					r.Header.Set(header, "test")
				}
				out := httptest.NewRecorder()
				handler.ServeHTTP(out, r)
				if out.Code != want {
					t.Fatalf("%s %s: %d %s", method, path, out.Code, out.Body.String())
				}
				return out
			}
			list := call("GET", base+"?tags=delete:yes", read.Token, "", 200)
			var listed struct {
				Result []struct {
					ID string `json:"id"`
				} `json:"result"`
			}
			must(json.Unmarshal(list.Body.Bytes(), &listed))
			names := []string{}
			for _, item := range listed.Result {
				names = append(names, item.ID)
			}
			sort.Strings(names)
			if len(names) != 2 || names[0] != "a" || names[1] != "b" {
				t.Fatal("listing did not select latest tags in its namespace")
			}
			call("DELETE", base+"?tags=delete:yes", read.Token, "", 403)
			call("DELETE", strings.Replace(base, org.ID, otherOrg.ID, 1)+"?tags=delete:yes", write.Token, "", 403)
			for _, query := range []string{"", "?tags=", "?tags=delete:maybe", "?tags=delete:yes,keep:no", "?tags=delete:yes&tags=keep:no", "?tags=delete:yes&page=1"} {
				call("DELETE", base+query, write.Token, "", 422)
			}
			for _, header := range []string{"Idempotency-Key", "If-Match", "X-Nanoflare-Customer-ID"} {
				call("DELETE", base+"?tags=delete:yes", write.Token, header, 422)
			}
			writer.failNext = true
			out := call("DELETE", base+"?tags=delete:yes", write.Token, "", 503)
			var response struct {
				Success bool                              `json:"success"`
				Result  nanoflare.NamespaceDeletionResult `json:"result"`
			}
			must(json.Unmarshal(out.Body.Bytes(), &response))
			if response.Success || response.Result.DeletedCount == nil || *response.Result.DeletedCount != 2 || response.Result.State != "committed" || response.Result.ConfigRefreshed {
				t.Fatal("config failure hid committed deletion", out.Body.String())
			}
			for _, app := range []nanoflare.App{a, b} {
				if _, err = repo.GetApp(app.ID); !errors.Is(err, nanoflare.ErrAppNotFound) {
					t.Fatal("selected Worker survived committed batch", err)
				}
			}
			for _, item := range []struct{ app, revision string }{{a.ID, aFirst.ID}, {b.ID, bFirst.ID}} {
				if _, err = repo.GetDeploymentArtifact(item.app, item.revision); !errors.Is(err, nanoflare.ErrArtifactNotFound) {
					t.Fatal("deleted Worker retained artifact", err)
				}
			}
			for _, app := range []nanoflare.App{keep, other, foreign, platform} {
				_, err = repo.GetApp(app.ID)
				must(err)
			}
			_, err = service.GetKVNamespaceForOrg(org.ID, kv.ID)
			must(err)
			out = call("DELETE", base+"?tags=delete:yes", write.Token, "", 200)
			must(json.Unmarshal(out.Body.Bytes(), &response))
			if response.Result.DeletedCount == nil || *response.Result.DeletedCount != 0 || !response.Result.ConfigRefreshed {
				t.Fatal("empty retry did not reconcile config")
			}
			uncertain, _ := create(org.ID, ns.ID, "uncertain", "alice", "uncertain")
			repo.failAfterDelete = true
			out = call("DELETE", base+"?tags=uncertain:yes", write.Token, "", 503)
			var unknown struct {
				Result map[string]json.RawMessage `json:"result"`
			}
			must(json.Unmarshal(out.Body.Bytes(), &unknown))
			if string(unknown.Result["nanoflare_deletion_state"]) != `"unverified"` || unknown.Result["deleted_count"] != nil {
				t.Fatal("unknown commit outcome reported an invented count", out.Body.String())
			}
			if _, err = repo.GetApp(uncertain.ID); !errors.Is(err, nanoflare.ErrAppNotFound) {
				t.Fatal("unknown commit fixture did not commit")
			}
			call("DELETE", base+"?tags=uncertain:yes", write.Token, "", 200)
			call("DELETE", base+"?tags=keep:no", write.Token, "", 200)
			_, err = repo.GetApp(keep.ID)
			must(err)
			_, err = repo.GetApp(platform.ID)
			must(err)
		})
	}
}
