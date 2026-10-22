package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/cli"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestPlatformCLIControlFlow(t *testing.T) {
	repo := nanoflare.NewStore()
	auth := nanoflare.NewControlAuthService(repo, "platform-cli-test")
	auth.SetTestHashCost(4)
	session, err := auth.Signup(nanoflare.SignupInput{Email: "cli@example.com", Password: "password", OrganizationName: "CLI"})
	if err != nil {
		t.Fatal(err)
	}
	org, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "platform-cli"})
	if err != nil {
		t.Fatal(err)
	}
	service := nanoflare.NewService(repo, discardWriter{})
	service.SetPlatformDispatchEnabled(true)
	server := httptest.NewServer(NewServerWithRuntime(service, nil, "", nil, auth, nil))
	defer server.Close()
	t.Setenv("NANOFLARE_TOKEN", session.Token)
	t.Setenv("NANOFLARE_ORG_ID", org.ID)
	t.Setenv("NANOFLARE_AUTH_STORE", filepath.Join(t.TempDir(), "auth.json"))
	run := func(args ...string) []byte {
		t.Helper()
		var stdout, stderr bytes.Buffer
		runner := cli.NewRunner(&stdout, &stderr)
		args = append([]string{"platform", "--api-url", server.URL}, args...)
		if err := runner.Run(args); err != nil {
			t.Fatalf("%v: %v (%s)", args, err, stderr.String())
		}
		return stdout.Bytes()
	}
	var ns nanoflare.DispatchNamespace
	if err = json.Unmarshal(run("namespace", "create", "staging", "--customer-mode", "platform"), &ns); err != nil {
		t.Fatal(err)
	}
	var worker nanoflare.App
	if err = json.Unmarshal(run("script", "create", ns.ID, "alpha", "--customer-id", "a"), &worker); err != nil {
		t.Fatal(err)
	}
	if !worker.IsPrivate() || !worker.IsDynamic() || worker.Hostname != "" {
		t.Fatalf("wrong private identity: %#v", worker)
	}
	var capabilities nanoflare.RuntimeCapabilities
	if err = json.Unmarshal(run("capabilities", worker.ID), &capabilities); err != nil {
		t.Fatal(err)
	}
	if !capabilities.Enabled || capabilities.CPUAccounting || capabilities.CPULimit || capabilities.CacheAPI != "unavailable" || capabilities.RawTCP != "unavailable" || capabilities.Limits.Subrequests != 100 {
		t.Fatalf("unsafe capability claim: %#v", capabilities)
	}
	service.SetPlatformDispatchEnabled(false)
	if err = json.Unmarshal(run("capabilities", worker.ID), &capabilities); err != nil || capabilities.Enabled {
		t.Fatalf("disabled capability: %#v %v", capabilities, err)
	}
	service.SetPlatformDispatchEnabled(true)
	kv, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: org.ID, Name: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(name string, value any) string {
		t.Helper()
		data, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		path := filepath.Join(dir, name)
		if e = os.WriteFile(path, data, 0600); e != nil {
			t.Fatal(e)
		}
		return path
	}
	grants := nanoflare.NamespaceGrantsInput{ExpectedRevision: 1, Grants: []nanoflare.NamespaceResourceGrant{{Kind: "kv", ResourceID: kv.ID, CustomerID: "a"}}}
	run("grants", "set", ns.ID, write("grants.json", grants))
	input := nanoflare.DeployInput{IdempotencyKey: "cli-v1", Format: "modules", CompatibilityDate: "2025-12-10", Entrypoint: "worker.js", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {fetch(){return new Response("v1")}}`}}, KVNamespaces: []nanoflare.KVBinding{{Binding: "STATE", ID: kv.ID}}}
	var first nanoflare.WorkerDeployment
	if err = json.Unmarshal(run("deploy", worker.ID, write("deploy.json", input)), &first); err != nil {
		t.Fatal(err)
	}
	input.IdempotencyKey = "cli-v2"
	input.Files[0].Content = strings.ReplaceAll(input.Files[0].Content, "v1", "v2")
	run("deploy", worker.ID, write("deploy.json", input))
	run("rollback", worker.ID, first.ID)
	active, err := repo.ActiveDeploymentsForApp(worker.ID)
	if err != nil || len(active) != 1 || active[0].Deployment.ID != first.ID {
		t.Fatalf("rollback: %#v %v", active, err)
	}
	var publications []nanoflare.PublicationResult
	if err = json.Unmarshal(run("publications", worker.ID), &publications); err != nil {
		t.Fatal(err)
	}
	if len(publications) != 2 || publications[0].RuntimeReady || publications[1].RuntimeReady {
		t.Fatalf("publication readiness: %#v", publications)
	}
	for _, p := range publications {
		if p.Stage != "route_published" {
			t.Fatalf("publication stage: %s", p.Stage)
		}
	}
	manifest := run("artifact", worker.ID, first.ID)
	storedWorker, e := repo.GetApp(worker.ID)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(manifest), storedWorker.RuntimeToken) || !strings.Contains(string(manifest), `"digest"`) {
		t.Fatal("unsafe artifact manifest")
	}
	run("namespace", "rename", ns.ID, "renamed")
	run("namespace", "policies", ns.ID)
	run("script", "list", ns.ID)
	run("deployments", worker.ID)
	grants.ExpectedRevision = 2
	grants.Grants = nil
	run("grants", "set", ns.ID, write("grants.json", grants))
	run("script", "delete", ns.ID, "alpha")
	run("namespace", "delete", ns.ID)
}
