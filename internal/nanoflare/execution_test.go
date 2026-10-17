package nanoflare

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type executionTestDB struct{ DBExecutor }

func (executionTestDB) Execute(string, DBQueryRequest) (DBQueryResponse, error) {
	return DBQueryResponse{First: "old database"}, nil
}
func (executionTestDB) Stats(string) (DatabaseRuntimeStats, error) {
	return DatabaseRuntimeStats{}, nil
}

func executionFixture(t *testing.T) *ExecutionSnapshot {
	t.Helper()
	s, err := FreezeExecution(ActiveDeployment{
		App: App{ID: "a", SecretValues: map[string]string{"SECRET": "old"}},
		Deployment: Deployment{ID: "d", AppID: "a", Vars: map[string]json.RawMessage{"VALUE": json.RawMessage(`"old"`)},
			Files:                []WorkerFile{{Name: "index.js", Content: "old"}},
			Assets:               []AssetFile{{Path: "/index.html", Data: []byte("old")}},
			KVNamespaces:         []KVBinding{{Binding: "KV", ID: "kv"}},
			Databases:            []DatabaseBinding{{Binding: "DB", DatabaseID: "db"}},
			ObjectStorageBuckets: []ObjectStorageBucketBinding{{Binding: "BUCKET", BucketID: "bucket"}},
			AssetConfig:          AssetConfig{Binding: "ASSETS", RunWorkerFirst: RunWorkerFirst{"/api/*"}},
		},
	}, "compile-option")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestExecutionSnapshotFreezesEnvironmentAndCollections(t *testing.T) {
	s := executionFixture(t)
	a := s.ActiveDeployment()
	original := a
	frozen, err := FreezeExecution(a, "compile-option")
	if err != nil {
		t.Fatal(err)
	}
	a.App.SecretValues["SECRET"] = "new"
	a.Deployment.Vars["VALUE"][1] = 'n'
	a.Deployment.Files[0].Content = "new"
	a.Deployment.Assets[0].Data[0] = 'n'
	a.Deployment.KVNamespaces[0].ID = "other"
	a.Deployment.AssetConfig.RunWorkerFirst[0] = "/*"
	got := frozen.ActiveDeployment()
	if got.App.SecretValues["SECRET"] != "old" || string(got.Deployment.Vars["VALUE"]) != `"old"` || got.Deployment.Files[0].Content != "old" || string(got.Deployment.Assets[0].Data) != "old" || got.Deployment.KVNamespaces[0].ID != "kv" || got.Deployment.AssetConfig.RunWorkerFirst[0] != "/api/*" {
		t.Fatal("snapshot aliases source")
	}
	got.App.SecretValues["SECRET"] = "accessor mutation"
	if frozen.ActiveDeployment().App.SecretValues["SECRET"] != "old" {
		t.Fatal("snapshot aliases accessor")
	}
	// original intentionally aliases the mutated input; each state dimension must
	// contribute to identity, especially secrets hidden by ActiveDeployment JSON.
	other, err := FreezeExecution(original, "compile-option")
	if err != nil {
		t.Fatal(err)
	}
	if other.ID() == frozen.ID() {
		t.Fatal("changed execution retained identity")
	}
	changedSecret := frozen.ActiveDeployment()
	changedSecret.App.SecretValues["SECRET"] = "rotated"
	other, _ = FreezeExecution(changedSecret, "compile-option")
	if other.ID() == frozen.ID() {
		t.Fatal("secrets excluded from identity")
	}
	other, _ = FreezeExecution(frozen.ActiveDeployment(), "other-option")
	if other.ID() == frozen.ID() {
		t.Fatal("compile options excluded from identity")
	}
	for _, invalid := range []ActiveDeployment{{}, {App: App{ID: "a"}, Deployment: Deployment{ID: "d", AppID: "b"}}} {
		if _, err := FreezeExecution(invalid); err == nil {
			t.Fatal("accepted invalid identity")
		}
	}
}

func TestExecutionIdentityIgnoresRoutingAndDisplayMetadata(t *testing.T) {
	s := executionFixture(t)
	a := s.ActiveDeployment()
	a.SocketPath = "/other/runtime.sock"
	a.TrafficPercent = 75
	a.App.Name = "Renamed app"
	a.App.Hostname = "new.example.com"
	a.App.Auth.ProtectedRoutes = []string{"/private/*"}
	a.App.ExternalID = "external"
	a.App.OAuthClientID = "client"
	a.App.CreatedBy = "someone"
	a.App.CreatedAt = time.Now()
	a.App.RuntimeToken = "rotated legacy credential"
	a.Deployment.Port = 6553
	a.Deployment.CommitHash = "another commit"
	a.Deployment.CommitMessage = "metadata"
	a.Deployment.CreatedBy = "someone else"
	a.Deployment.CreatedAt = time.Now()
	updated, err := FreezeExecution(a, "compile-option")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID() != s.ID() {
		t.Fatal("routing or display metadata changed execution identity")
	}
	if updated.ActiveDeployment().App.Hostname != a.App.Hostname {
		t.Fatal("captured metadata lost")
	}
	a.App.OrgID = "another-org"
	updated, _ = FreezeExecution(a, "compile-option")
	if updated.ID() == s.ID() {
		t.Fatal("tenant identity excluded")
	}
}

func TestExecutionAdmissionRetirementAndLifetimePins(t *testing.T) {
	r := NewExecutionRegistry()
	s := executionFixture(t)
	r.Publish(s)
	l, err := r.Admit(s.ID())
	if err != nil {
		t.Fatal(err)
	}
	stream, _ := l.Pin()
	background, _ := l.Pin()
	websocket, _ := l.Pin()
	r.Retire(s.ID())
	if _, err := r.Admit(s.ID()); !errors.Is(err, ErrExecutionRetired) {
		t.Fatalf("retired admission: %v", err)
	}
	l.Release()
	l.Release()
	for _, pin := range []*ExecutionLease{stream, background, websocket} {
		if _, err := r.Authorize(pin.Token(), ExecutionKV, "kv"); err != nil {
			t.Fatal("admitted work lost authority", err)
		}
		pin.Release()
	}
	if _, err := r.Authorize(l.Token(), ExecutionKV, "kv"); !errors.Is(err, ErrInvalidCapability) {
		t.Fatal("released capability remains usable")
	}
	if _, err := l.Pin(); !errors.Is(err, ErrInvalidCapability) {
		t.Fatal("released capability can be pinned")
	}
	if len(r.tokens) != 0 || len(r.revisions) != 0 {
		t.Fatal("retired execution retained after final pin")
	}
}

func TestExecutionCapabilitiesGrantOnlyFrozenResources(t *testing.T) {
	r := NewExecutionRegistry()
	s := executionFixture(t)
	r.Publish(s)
	l, _ := r.Admit(s.ID())
	defer l.Release()
	for kind, id := range map[ExecutionResourceKind]string{ExecutionKV: "kv", ExecutionDatabase: "db", ExecutionObject: "bucket", ExecutionAssets: "d"} {
		if _, err := r.Authorize(l.Token(), kind, id); err != nil {
			t.Fatalf("declared %s: %v", kind, err)
		}
		if _, err := r.Authorize(l.Token(), kind, "other"); !errors.Is(err, ErrInvalidCapability) {
			t.Fatalf("undeclared %s: %v", kind, err)
		}
		if _, err := r.Authorize(l.Token(), kind, ""); !errors.Is(err, ErrInvalidCapability) {
			t.Fatalf("empty %s: %v", kind, err)
		}
	}
	if _, err := r.Authorize("nfexec_forged", ExecutionKV, "kv"); !errors.Is(err, ErrInvalidCapability) {
		t.Fatal("forged capability accepted")
	}
}

func TestExecutionRetiredResourcesAndObjectRetention(t *testing.T) {
	store := NewStore()
	objects := newMemoryObjectStore()
	s := NewServiceWithObjects(store, &recordingWriter{}, objects)
	s.SetDBExecutor(executionTestDB{})
	if err := store.CreateApp(App{ID: "a", RuntimeToken: "legacy"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateDatabase(Database{ID: "db"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateObjectStorageBucket(ObjectStorageBucket{ID: "bucket"}); err != nil {
		t.Fatal(err)
	}
	snapshot := executionFixture(t)
	active := snapshot.ActiveDeployment()
	active.Deployment.AssetConfig.Binding = "" // Workerd's default ASSETS binding.
	active.Deployment.ObjectKey = "shared/bundle.json"
	active.Deployment.Assets[0].ObjectKey = "shared/index.html"
	if _, err := objects.Put("a", active.Deployment.ObjectKey, "application/json", []byte("bundle")); err != nil {
		t.Fatal(err)
	}
	if _, err := objects.Put("a", active.Deployment.Assets[0].ObjectKey, "text/html", []byte("old asset")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := FreezeExecution(active)
	if err != nil {
		t.Fatal(err)
	}
	s.Executions().Publish(snapshot)
	lease, err := s.Executions().Admit(snapshot.ID())
	if err != nil {
		t.Fatal(err)
	}
	background, err := lease.Pin()
	if err != nil {
		t.Fatal(err)
	}
	s.Executions().Retire(snapshot.ID())
	lease.Release()
	// No active deployment exists: authorization must come from the admitted
	// frozen execution, never a mutable active revision or the legacy app token.
	response, err := s.DBExecute(lease.Token(), "db", DBQueryRequest{})
	if err != nil || response.First != "old database" {
		t.Fatalf("retired DB: %#v %v", response, err)
	}
	asset, err := s.AssetFetch(lease.Token(), "d", "/index.html")
	if err != nil || string(asset.Body) != "old asset" {
		t.Fatalf("retired asset: %#v %v", asset, err)
	}
	if _, err := s.ObjectPut(lease.Token(), "bucket", "key", "text/plain", []byte("old object")); err != nil {
		t.Fatal(err)
	}
	body, ok, err := s.ObjectGet(lease.Token(), "bucket", "key")
	if err != nil || !ok || string(body.Body) != "old object" {
		t.Fatalf("retired object: %#v %v %v", body, ok, err)
	}
	if _, err := s.AssetFetch(lease.Token(), "replacement", "/index.html"); !errors.Is(err, ErrInvalidCapability) {
		t.Fatal("cross revision asset allowed", err)
	}
	// Cleanup of a separate revision sharing these keys must also retain them.
	shared := active.Deployment
	shared.ID = "shared-revision"
	s.cleanupDeploymentObject(shared)
	s.cleanupDeploymentAssets(shared)
	if _, err := objects.Head("a", active.Deployment.ObjectKey); err != nil {
		t.Fatal("leased bundle collected", err)
	}
	if _, err := objects.Head("a", active.Deployment.Assets[0].ObjectKey); err != nil {
		t.Fatal("leased asset collected", err)
	}
	background.Release()
	if _, err := s.DBExecute(lease.Token(), "db", DBQueryRequest{}); !errors.Is(err, ErrInvalidCapability) {
		t.Fatal("released DB authority allowed", err)
	}
	if _, err := s.AssetFetch(lease.Token(), "d", "/index.html"); !errors.Is(err, ErrInvalidCapability) {
		t.Fatal("released asset authority allowed", err)
	}
	s.cleanupDeploymentObject(shared)
	s.cleanupDeploymentAssets(shared)
	if _, err := objects.Head("a", active.Deployment.ObjectKey); !errors.Is(err, ErrObjectNotFound) {
		t.Fatal("unreferenced bundle retained", err)
	}
	if _, err := objects.Head("a", active.Deployment.Assets[0].ObjectKey); !errors.Is(err, ErrObjectNotFound) {
		t.Fatal("unreferenced asset retained", err)
	}
}

func TestExecutionRegistrationRejectsCrossTenantResources(t *testing.T) {
	store := NewStore()
	s := NewService(store, &recordingWriter{})
	if err := store.CreateApp(App{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateKVNamespace(KVNamespace{ID: "kv", OrgID: "another-org"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateDatabase(Database{ID: "db", OrgID: "another-org"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateObjectStorageBucket(ObjectStorageBucket{ID: "bucket", OrgID: "another-org"}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []Deployment{
		{ID: "d", AppID: "a", KVNamespaces: []KVBinding{{ID: "kv"}}},
		{ID: "d", AppID: "a", Databases: []DatabaseBinding{{DatabaseID: "db"}}},
		{ID: "d", AppID: "a", ObjectStorageBuckets: []ObjectStorageBucketBinding{{BucketID: "bucket"}}},
	} {
		if _, err := s.RegisterExecution(ActiveDeployment{App: App{ID: "a"}, Deployment: d}, ""); !errors.Is(err, ErrInvalidCapability) {
			t.Fatal("cross tenant registration accepted", err)
		}
	}
}
