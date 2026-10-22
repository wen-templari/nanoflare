package nanoflare

import (
	"context"
	"encoding/json"
	"testing"
)

func TestArtifactsPinCodeAssetsSecretsAndVars(t *testing.T) {
	repo := NewStore()
	objects := newMemoryObjectStore()
	service := NewServiceWithObjects(repo, &recordingWriter{}, objects)
	codec, err := NewSecretCodec("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	service.SetSecretCodec(codec)
	app, err := service.CreateApp(CreateAppInput{Name: "immutable", Hostname: "immutable.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.PutSecret(app.ID, "TOKEN", "old"); err != nil {
		t.Fatal(err)
	}
	input := DeployInput{IdempotencyKey: "one", Files: []WorkerFile{{Path: "worker.js", Content: "export default {}"}}, Assets: []AssetFile{{Path: "index.html", Data: []byte("old asset"), ContentType: "text/html"}}, CompatibilityDate: "2025-12-10"}
	first, err := service.Deploy(app.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	input.IdempotencyKey = "equivalent"
	equivalent, err := service.Deploy(app.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	a, err := repo.GetDeploymentArtifact(app.ID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := repo.GetDeploymentArtifact(app.ID, equivalent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest != b.Digest {
		t.Fatal("storage keys or deployment identity changed the content digest")
	}
	input.IdempotencyKey = "two"
	input.Files = []WorkerFile{{Path: "worker.js", Content: "export default {fetch(){return new Response('new')}}"}}
	input.Assets = []AssetFile{{Path: "index.html", Data: []byte("new asset"), ContentType: "text/html"}}
	second, err := service.Deploy(app.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	service.SetRuntimeDeploymentChecker(func(_ context.Context, appID, id string) (bool, error) {
		return appID == app.ID && (id == first.ID || id == second.ID), nil
	})
	oldToken := RuntimeCapability(ActiveDeployment{App: app, Deployment: first}, RuntimeAssets, first.ID)
	response, err := service.AssetFetch(oldToken, first.ID, "/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Body) != "old asset" {
		t.Fatal("draining invocation mixed new assets")
	}
	newToken := RuntimeCapability(ActiveDeployment{App: app, Deployment: second}, RuntimeAssets, second.ID)
	response, err = service.AssetFetch(newToken, second.ID, "/index.html")
	if err != nil || string(response.Body) != "new asset" {
		t.Fatalf("new asset: %s %v", response.Body, err)
	}
	if _, err = service.AssetFetch(oldToken, second.ID, "/index.html"); err == nil {
		t.Fatal("old capability read a new revision")
	}
	if _, err = objects.Put(app.ID, first.Assets[0].ObjectKey, "text/html", []byte("corrupted")); err != nil {
		t.Fatal(err)
	}
	if _, err = service.AssetFetch(oldToken, first.ID, "/index.html"); err == nil {
		t.Fatal("asset corruption was served")
	}
	if _, err = objects.Put(app.ID, first.Assets[0].ObjectKey, "text/html", []byte("old asset")); err != nil {
		t.Fatal(err)
	}
	// A vars update from a rolled-back revision inherits its sealed secret version,
	// even when the mutable secret store contains a newer desired version.
	if err = service.PutSecret(app.ID, "TOKEN", "new"); err != nil {
		t.Fatal(err)
	}
	if err = repo.SetActive(app.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	vars, err := service.UpdateVarsForOrg(app.OrgID, app.ID, UpdateVarsInput{ExpectedDeploymentID: first.ID, IdempotencyKey: "vars", Vars: map[string]json.RawMessage{"ENV": json.RawMessage(`"preview"`)}})
	if err != nil {
		t.Fatal(err)
	}
	active, err := service.ActiveDeployments()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].Deployment.ID != vars.ID || active[0].App.SecretValues["TOKEN"] != "old" {
		t.Fatal("vars update mixed latest secrets with rollback snapshot")
	}
}
