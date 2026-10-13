package nanoflare

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
)

type varsTestWriter struct {
	failNext bool
	last     []ActiveDeployment
}

func (w *varsTestWriter) Write(active []ActiveDeployment) error {
	// Model a writer that changes runtime state before failing.
	w.last = active
	if w.failNext {
		w.failNext = false
		return errors.New("publication failed")
	}
	return nil
}

func varsFixture(t *testing.T) (*Service, *Store, *memoryObjectStore, *varsTestWriter, App, Deployment) {
	t.Helper()
	store := NewStore()
	if err := store.CreateOrganization(Organization{ID: "org", Name: "Vars org"}); err != nil {
		t.Fatal(err)
	}
	objects := newMemoryObjectStore()
	writer := &varsTestWriter{}
	service := NewServiceWithObjects(store, writer, objects)
	codec, err := NewSecretCodec("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	service.SetSecretCodec(codec)
	app, err := service.CreateApp(CreateAppInput{OrgID: "org", Name: "Vars", Hostname: "vars.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PutSecret(app.ID, "SECRET", "secret-value"); err != nil {
		t.Fatal(err)
	}
	kv, err := service.CreateKVNamespace(CreateKVNamespaceInput{OrgID: "org", Name: "vars-kv"})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := service.DeployForOrg("org", app.ID, DeployInput{
		Files:      []WorkerFile{{Path: "worker.js", Content: "export default { fetch() {} }"}},
		Entrypoint: "worker.js", Format: "modules", CompatibilityDate: "2025-12-10",
		CompatibilityFlags: []string{"nodejs_compat"},
		Assets:             []AssetFile{{Path: "index.html", Data: []byte("original asset")}},
		AssetConfig:        AssetConfig{Binding: "ASSETS"},
		KVNamespaces:       []KVBinding{{Binding: "KV", ID: kv.ID}},
		Vars:               map[string]json.RawMessage{"OLD": json.RawMessage(`"old"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, store, objects, writer, app, dep
}

func varsInput(base, key string) UpdateVarsInput {
	return UpdateVarsInput{Vars: map[string]json.RawMessage{"TEXT": json.RawMessage(`"new"`), "JSON": json.RawMessage(`{"beta":true}`)}, ExpectedDeploymentID: base, IdempotencyKey: key, CommitMessage: "publication-123", CreatedBy: "owner"}
}

func TestVarsUpdatePreservesArtifactAndRetries(t *testing.T) {
	service, store, objects, _, app, base := varsFixture(t)
	beforeObjects := len(objects.objects)
	input := varsInput(base.ID, "first")
	result, err := service.UpdateVarsForOrg("org", app.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.ID == base.ID || result.BaseDeploymentID != base.ID || result.CommitMessage != input.CommitMessage || result.CreatedBy != "owner" {
		t.Fatalf("result = %#v", result)
	}
	live, _, ok, err := service.WorkerRuntimeDeployment(app.ID, "/")
	if err != nil || !ok {
		t.Fatalf("runtime = %v, %v", ok, err)
	}
	next := live.Deployment
	if next.ID != result.ID || next.Port != 0 || live.App.SecretValues["SECRET"] != "secret-value" {
		t.Fatalf("live = %#v", live)
	}
	if !reflect.DeepEqual(next.Vars, input.Vars) {
		t.Fatalf("vars = %#v", next.Vars)
	}
	// Compare every setting and artifact reference apart from intentional metadata.
	expected := base
	expected.ID = next.ID
	expected.Port = next.Port
	expected.CreatedAt = next.CreatedAt
	expected.CreatedBy = next.CreatedBy
	expected.CommitMessage = next.CommitMessage
	expected.Vars = next.Vars
	if !reflect.DeepEqual(next, expected) {
		t.Fatalf("settings changed:\nnext=%#v\nexpected=%#v", next, expected)
	}
	if len(objects.objects) != beforeObjects {
		t.Fatal("configuration update uploaded objects")
	}
	replay, err := service.UpdateVarsForOrg("org", app.ID, input)
	if err != nil || replay != result {
		t.Fatalf("replay = %#v, %v", replay, err)
	}
	// A later update must not be overwritten by replaying the first.
	clear := UpdateVarsInput{Vars: map[string]json.RawMessage{}, ExpectedDeploymentID: result.ID, IdempotencyKey: "clear"}
	cleared, err := service.UpdateVarsForOrg("org", app.ID, clear)
	if err != nil {
		t.Fatal(err)
	}
	replay, err = service.UpdateVarsForOrg("org", app.ID, input)
	if err != nil || replay != result {
		t.Fatalf("superseded replay = %#v, %v", replay, err)
	}
	live, _, _, err = service.WorkerRuntimeDeployment(app.ID, "/")
	if err != nil || live.Deployment.ID != cleared.ID || len(live.Deployment.Vars) != 0 {
		t.Fatalf("cleared runtime=%#v, %v", live, err)
	}
	records, _ := store.ListDeployments()
	if len(records) != 3 {
		t.Fatalf("retries created deployments: %d", len(records))
	}
	if err := service.DeleteApp(app.ID); err != nil {
		t.Fatal(err)
	}
	if len(objects.objects) != 0 {
		t.Fatal("worker deletion leaked shared artifacts")
	}
}

func TestVarsUpdateConflictsAndValidation(t *testing.T) {
	service, store, _, _, app, base := varsFixture(t)
	for _, name := range []string{"SECRET", "KV", "ASSETS"} {
		input := varsInput(base.ID, "collision-"+name)
		input.Vars = map[string]json.RawMessage{name: json.RawMessage(`"collision"`)}
		if _, err := service.UpdateVarsForOrg("org", app.ID, input); !errors.Is(err, ErrInvalidVarsUpdate) {
			t.Fatalf("collision %s = %v", name, err)
		}
	}
	for name, vars := range map[string]map[string]json.RawMessage{
		"missing": nil, "blank": {" ": json.RawMessage(`1`)}, "invalid": {"X": json.RawMessage(`oops`)}, "normalized duplicate": {" X ": json.RawMessage(`1`), "X": json.RawMessage(`2`)},
	} {
		input := varsInput(base.ID, name)
		input.Vars = vars
		if _, err := service.UpdateVarsForOrg("org", app.ID, input); !errors.Is(err, ErrInvalidVarsUpdate) {
			t.Fatalf("%s = %v", name, err)
		}
	}
	if _, err := service.UpdateVarsForOrg("other-org", app.ID, varsInput(base.ID, "ownership")); !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("ownership = %v", err)
	}
	if _, err := service.UpdateVarsForOrg("org", app.ID, varsInput("stale", "stale")); !errors.Is(err, ErrDeploymentConflict) {
		t.Fatalf("stale = %v", err)
	}
	result, err := service.UpdateVarsForOrg("org", app.ID, varsInput(base.ID, "key"))
	if err != nil {
		t.Fatal(err)
	}
	changed := varsInput(base.ID, "key")
	changed.CommitMessage = "different"
	if _, err := service.UpdateVarsForOrg("org", app.ID, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("key reuse = %v", err)
	}
	if err := store.SetActiveTraffic(app.ID, []DeploymentTraffic{{ID: base.ID, TrafficPercent: 50}, {ID: result.ID, TrafficPercent: 50}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateVarsForOrg("org", app.ID, varsInput(result.ID, "split")); !errors.Is(err, ErrDeploymentConflict) {
		t.Fatalf("split = %v", err)
	}
	if err := store.SetActiveTraffic(app.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateVarsForOrg("org", app.ID, varsInput(result.ID, "inactive")); !errors.Is(err, ErrDeploymentConflict) {
		t.Fatalf("inactive = %v", err)
	}
}

func TestVarsRolloutFailureRestoresRuntimeAndSharedObjects(t *testing.T) {
	service, store, objects, writer, app, base := varsFixture(t)
	writer.failNext = true
	input := varsInput(base.ID, "fail")
	if _, err := service.UpdateVarsForOrg("org", app.ID, input); err == nil {
		t.Fatal("expected rollout failure")
	}
	if len(writer.last) != 1 || writer.last[0].Deployment.ID != base.ID {
		t.Fatal("runtime configuration was not restored")
	}
	live, _, ok, err := service.WorkerRuntimeDeployment(app.ID, "/")
	if err != nil || !ok || live.Deployment.ID != base.ID {
		t.Fatalf("restored runtime=%#v, %v", live, err)
	}
	if _, err := objects.Get(app.ID, base.ObjectKey); err != nil {
		t.Fatalf("bundle deleted: %v", err)
	}
	if _, err := objects.Get(app.ID, base.Assets[0].ObjectKey); err != nil {
		t.Fatalf("asset deleted: %v", err)
	}
	records, _ := store.ListDeployments()
	if len(records) != 1 {
		t.Fatalf("records=%d", len(records))
	}
	if _, err := service.UpdateVarsForOrg("org", app.ID, input); !errors.Is(err, ErrVarsRolloutFailed) {
		t.Fatalf("failed replay = %v", err)
	}
	// Existing secret rollouts must also leave the original artifacts intact.
	writer.failNext = true
	if err := service.PutSecret(app.ID, "NEW_SECRET", "new"); err == nil {
		t.Fatal("expected secret rollout failure")
	}
	if _, err := objects.Get(app.ID, base.ObjectKey); err != nil {
		t.Fatal(err)
	}
	if _, err := objects.Get(app.ID, base.Assets[0].ObjectKey); err != nil {
		t.Fatal(err)
	}
}

func TestVarsUpdateConcurrentServicesAndPendingRecovery(t *testing.T) {
	service, store, objects, _, app, base := varsFixture(t)
	other := NewServiceWithObjects(store, &recordingWriter{}, objects)
	other.SetSecretCodec(service.secrets)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i, svc := range []*Service{service, other} {
		wg.Add(1)
		go func(i int, svc *Service) {
			defer wg.Done()
			<-start
			_, err := svc.UpdateVarsForOrg("org", app.ID, varsInput(base.ID, []string{"a", "b"}[i]))
			errs <- err
		}(i, svc)
	}
	close(start)
	wg.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrDeploymentConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	// Simulate a process stopping after atomic activation but before publication.
	active, _ := store.ActiveDeploymentsForApp(app.ID)
	current := active[0].Deployment
	input, hash, err := normalizeVarsUpdate(varsInput(current.ID, "pending"))
	if err != nil {
		t.Fatal(err)
	}
	next := current
	next.ID = "pending-revision"
	next.Port++
	next.Vars = input.Vars
	op := VarsUpdateOperation{AppID: app.ID, Key: input.IdempotencyKey, Fingerprint: hash, Status: "pending", Result: VarsUpdateResult{ID: next.ID, BaseDeploymentID: current.ID}}
	if err := store.BeginVarsUpdate(op, next, current.ID); err != nil {
		t.Fatal(err)
	}
	result, err := other.UpdateVarsForOrg("org", app.ID, input)
	if err != nil || result.ID != next.ID {
		t.Fatalf("recovered=%#v, %v", result, err)
	}
	saved, _, _ := store.VarsUpdate(app.ID, op.Key)
	if saved.Status != "succeeded" {
		t.Fatalf("saved=%#v", saved)
	}
	// A pending rollback with a removed revision must recover as failure.
	next.ID = "removed-revision"
	op.Key = "removed"
	op.Result.ID = next.ID
	input.IdempotencyKey = op.Key
	input.ExpectedDeploymentID = result.ID
	_, op.Fingerprint, err = normalizeVarsUpdate(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginVarsUpdate(op, next, result.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetActive(app.ID, result.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDeployment(next.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := other.UpdateVarsForOrg("org", app.ID, input); !errors.Is(err, ErrVarsRolloutFailed) {
		t.Fatalf("removed recovery=%v", err)
	}
}

func TestUpdateVarsInputDecoding(t *testing.T) {
	for _, body := range []string{`{}`, `{"vars":null}`, `{"vars":[]}`, `{"vars":{},"files":[]}`} {
		var input UpdateVarsInput
		if err := json.Unmarshal([]byte(body), &input); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	var input UpdateVarsInput
	if err := json.Unmarshal([]byte(`{"vars":{},"expected_deployment_id":"base","idempotency_key":"key"}`), &input); err != nil {
		t.Fatal(err)
	}
	a := varsInput("base", "key")
	b := varsInput("base", "key")
	a.Vars["JSON"] = json.RawMessage(`{"z":1,"a":true}`)
	b.Vars["JSON"] = json.RawMessage(`{ "a":true, "z":1 }`)
	_, ha, _ := normalizeVarsUpdate(a)
	_, hb, _ := normalizeVarsUpdate(b)
	if ha != hb {
		t.Fatal("object order or whitespace changed fingerprint")
	}
}

func TestVarsUpdatePreservesAllBindingsAndTriggers(t *testing.T) {
	service, _, objects, _, app, base := varsFixture(t)
	db, err := service.CreateDatabase(CreateDatabaseInput{OrgID: "org", Name: "vars-db"})
	if err != nil {
		t.Fatal(err)
	}
	bucket, err := service.CreateObjectStorageBucket(CreateObjectStorageBucketInput{OrgID: "org", Name: "vars-objects"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := service.CreateApp(CreateAppInput{OrgID: "org", Name: "target", Hostname: "vars-target.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeployForOrg("org", target.ID, DeployInput{Files: base.Files, Entrypoint: base.Entrypoint, Format: base.Format, CompatibilityDate: base.CompatibilityDate}); err != nil {
		t.Fatal(err)
	}
	configured, err := service.DeployForOrg("org", app.ID, DeployInput{Files: base.Files, Entrypoint: base.Entrypoint, Format: base.Format, CompatibilityDate: base.CompatibilityDate, CompatibilityFlags: base.CompatibilityFlags, Assets: []AssetFile{{Path: "index.html", Data: []byte("original asset")}}, AssetConfig: base.AssetConfig, KVNamespaces: base.KVNamespaces, Databases: []DatabaseBinding{{Binding: "DB", DatabaseID: db.ID}}, ObjectStorageBuckets: []ObjectStorageBucketBinding{{Binding: "OBJECTS", BucketID: bucket.ID}}, Services: []ServiceBinding{{Binding: "SERVICE", Service: target.Name}}, DNS: DNSSelection{Profile: "custom"}, Triggers: TriggerConfig{Crons: []string{"*/5 * * * *"}}})
	if err != nil {
		t.Fatal(err)
	}
	count := len(objects.objects)
	for _, name := range []string{"DB", "OBJECTS", "SERVICE"} {
		input := varsInput(configured.ID, name)
		input.Vars = map[string]json.RawMessage{name: json.RawMessage(`true`)}
		if _, err := service.UpdateVarsForOrg("org", app.ID, input); !errors.Is(err, ErrInvalidVarsUpdate) {
			t.Fatalf("collision %s=%v", name, err)
		}
	}
	result, err := service.UpdateVarsForOrg("org", app.ID, varsInput(configured.ID, "all-settings"))
	if err != nil {
		t.Fatal(err)
	}
	active, err := service.ActiveDeployments()
	if err != nil {
		t.Fatal(err)
	}
	next := activeForApp(active, app.ID).Deployment
	if next.ID != result.ID || len(objects.objects) != count {
		t.Fatalf("new revision=%#v", next)
	}
	expected := configured
	expected.ID = next.ID
	expected.Port = next.Port
	expected.CreatedAt = next.CreatedAt
	expected.CreatedBy = next.CreatedBy
	expected.CommitMessage = next.CommitMessage
	expected.Vars = next.Vars
	if !reflect.DeepEqual(next, expected) {
		t.Fatalf("configuration changed:\nnext=%#v\nexpected=%#v", next, expected)
	}
}

type finishVarsFailRepo struct {
	*Store
	fail bool
}

func (r *finishVarsFailRepo) FinishVarsUpdate(appID, key, status string) error {
	if r.fail && status == "succeeded" {
		r.fail = false
		return errors.New("outcome write failed")
	}
	return r.Store.FinishVarsUpdate(appID, key, status)
}

func TestVarsRetryAfterOutcomeWriteFailure(t *testing.T) {
	service, store, objects, writer, app, base := varsFixture(t)
	repo := &finishVarsFailRepo{Store: store, fail: true}
	service.store = repo
	input := varsInput(base.ID, "lost-outcome")
	if _, err := service.UpdateVarsForOrg("org", app.ID, input); err == nil {
		t.Fatal("expected outcome write failure")
	}
	op, found, err := store.VarsUpdate(app.ID, input.IdempotencyKey)
	if err != nil || !found || op.Status != "pending" {
		t.Fatalf("op=%#v, %v", op, err)
	}
	recovered := NewServiceWithObjects(repo, writer, objects)
	recovered.SetSecretCodec(service.secrets)
	result, err := recovered.UpdateVarsForOrg("org", app.ID, input)
	if err != nil || result != op.Result {
		t.Fatalf("recovered=%#v, %v", result, err)
	}
	records, _ := store.ListDeployments()
	if len(records) != 2 {
		t.Fatalf("duplicate revision: %d", len(records))
	}
}

func TestVarsRecoverInterruptedRollbackBeforeRevisionDeletion(t *testing.T) {
	service, store, _, writer, app, base := varsFixture(t)
	input, hash, err := normalizeVarsUpdate(varsInput(base.ID, "interrupted-rollback"))
	if err != nil {
		t.Fatal(err)
	}
	next := base
	next.ID = "rollback-candidate"
	next.Port++
	next.Vars = input.Vars
	op := VarsUpdateOperation{AppID: app.ID, Key: input.IdempotencyKey, Fingerprint: hash, Status: "pending", Result: VarsUpdateResult{ID: next.ID, BaseDeploymentID: base.ID}}
	if err := store.BeginVarsUpdate(op, next, base.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishVarsUpdate(app.ID, op.Key, "rolling_back"); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after traffic restoration but before deleting the revision.
	if err := store.SetActive(app.ID, base.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateVarsForOrg("org", app.ID, input); !errors.Is(err, ErrVarsRolloutFailed) {
		t.Fatalf("rollback recovery=%v", err)
	}
	records, _ := store.ListDeployments()
	if len(records) != 1 || records[0].Deployment.ID != base.ID {
		t.Fatalf("records=%#v", records)
	}
	if len(writer.last) != 1 || writer.last[0].Deployment.ID != base.ID {
		t.Fatal("rollback did not restore runtime")
	}
	op, _, _ = store.VarsUpdate(app.ID, op.Key)
	if op.Status != "failed" {
		t.Fatalf("outcome=%s", op.Status)
	}
}
