package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/database"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

func TestCreateDeployAndScopedKV(t *testing.T) {
	dir := t.TempDir()
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	))
	server := NewServer(service)

	appOne := createApp(t, server, "App One", "one.example.com")
	appTwo := createApp(t, server, "App Two", "two.example.com")
	namespaceOne := createKVNamespace(t, server, "app-one")
	namespaceTwo := createKVNamespace(t, server, "app-two")
	deployWithKV(t, server, appOne.ID, []nanoflare.KVBinding{{Binding: "KV", ID: namespaceOne.ID}})
	deployWithKV(t, server, appTwo.ID, []nanoflare.KVBinding{{Binding: "KV", ID: namespaceTwo.ID}})
	tokens := runtimeTokens(t, store)
	kv := NewRuntimeKVServer(service)

	runtimeKVRequest(t, kv, http.MethodPut, "/color?urlencoded=true", tokens[appOne.ID], namespaceOne.ID, []byte("blue"), http.StatusNoContent)
	if got := runtimeKVRequest(t, kv, http.MethodGet, "/color?urlencoded=true", tokens[appOne.ID], namespaceOne.ID, nil, http.StatusOK); string(got) != "blue" {
		t.Fatalf("got %q, want app-one value", got)
	}
	runtimeKVRequest(t, kv, http.MethodGet, "/color?urlencoded=true", tokens[appTwo.ID], namespaceTwo.ID, nil, http.StatusNotFound)
	metrics, err := service.KVNamespaceMetrics(namespaceOne.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !metrics.Available || metrics.Reads != 1 || metrics.Writes != 1 {
		t.Fatalf("unexpected runtime KV metrics: %#v", metrics)
	}
}

func TestWorkerConsoleAPIs(t *testing.T) {
	dir := t.TempDir()
	bundle := `addEventListener("fetch", () => {});`
	outputBuffer := runtime.NewOutputBuffer()
	service := nanoflare.NewServiceWithConsole(nanoflare.NewStore(), config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	), nil, outputBuffer, fakeTraffic{})
	server := NewServer(service)
	app := createApp(t, server, "Console App", "console.example.com")
	deployContent(t, server, app.ID, []nanoflare.WorkerFile{{Path: "worker.js", Content: bundle}}, "")

	var detail nanoflare.WorkerDetail
	requestJSON(t, server, http.MethodGet, "/v1/workers/"+app.ID, http.StatusOK, &detail)
	if detail.Deployment == nil || detail.Deployment.Entrypoint != "worker.js" || detail.Deployment.BundleSize != int64(len(bundle)) {
		t.Fatalf("unexpected worker detail: %#v", detail)
	}
	if detail.Deployment.CompatibilityDate != "2025-12-10" {
		t.Fatalf("compatibility date = %q", detail.Deployment.CompatibilityDate)
	}
	outputBuffer.AppendScoped(app.ID, detail.Deployment.ID, "info", "runtime ready")

	var files []nanoflare.WorkerFile
	requestJSON(t, server, http.MethodGet, "/v1/workers/"+app.ID+"/files", http.StatusOK, &files)
	if len(files) != 1 || files[0].Name != "worker.js" || files[0].Content != bundle {
		t.Fatalf("unexpected worker files: %#v", files)
	}

	var output []nanoflare.WorkerOutputLine
	requestJSON(t, server, http.MethodGet, "/v1/workers/"+app.ID+"/output", http.StatusOK, &output)
	if len(output) != 1 || output[0].Message != "runtime ready" {
		t.Fatalf("unexpected worker output: %#v", output)
	}

	var traffic nanoflare.WorkerTraffic
	requestJSON(t, server, http.MethodGet, "/v1/workers/"+app.ID+"/traffic", http.StatusOK, &traffic)
	if !traffic.Available || traffic.RequestsPerSecond != 4.25 || len(traffic.Traffic) != 2 {
		t.Fatalf("unexpected worker traffic: %#v", traffic)
	}
	if traffic.DurationMsAvg != 12.5 || traffic.DurationMsP95 != 20 || traffic.DurationMsPerSecond != 1.5 || len(traffic.DurationSeries) != 2 {
		t.Fatalf("unexpected worker duration traffic: %#v", traffic)
	}

	var apps []nanoflare.App
	requestJSON(t, server, http.MethodGet, "/v1/workers", http.StatusOK, &apps)
	if len(apps) != 1 || apps[0].ID != app.ID {
		t.Fatalf("unexpected app list: %#v", apps)
	}
}

func TestSecretAPIsReturnMetadataOnly(t *testing.T) {
	dir := t.TempDir()
	service := nanoflare.NewServiceWithConsole(nanoflare.NewStore(), config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	), nil, fakeOutput{}, fakeTraffic{})
	codec, err := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	service.SetSecretCodec(codec)
	server := NewServer(service)
	app := createApp(t, server, "Console App", "console.example.com")

	requestJSONBytes(t, server, http.MethodPut, "/v1/workers/"+app.ID+"/secrets/DB_URL", []byte(`{"value":"postgres://secret"}`), http.StatusNoContent, nil)

	var secrets []nanoflare.Secret
	requestJSON(t, server, http.MethodGet, "/v1/workers/"+app.ID+"/secrets", http.StatusOK, &secrets)
	if len(secrets) != 1 || secrets[0].Name != "DB_URL" {
		t.Fatalf("unexpected secrets: %#v", secrets)
	}

	var detail nanoflare.WorkerDetail
	requestJSON(t, server, http.MethodGet, "/v1/workers/"+app.ID, http.StatusOK, &detail)
	if len(detail.Secrets) != 1 || detail.Secrets[0].Name != "DB_URL" {
		t.Fatalf("unexpected worker detail secrets: %#v", detail.Secrets)
	}
}

func TestWorkerConsoleKV(t *testing.T) {
	dir := t.TempDir()
	service := nanoflare.NewService(nanoflare.NewStore(), config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	))
	server := NewServer(service)
	appOne := createApp(t, server, "Console KV One", "console-kv-one.example.com")
	appTwo := createApp(t, server, "Console KV Two", "console-kv-two.example.com")
	namespaceOne := createKVNamespace(t, server, "console-one")
	namespaceTwo := createKVNamespace(t, server, "console-two")
	deployWithKV(t, server, appOne.ID, []nanoflare.KVBinding{{Binding: "KV", ID: namespaceOne.ID}})
	deployWithKV(t, server, appTwo.ID, []nanoflare.KVBinding{{Binding: "KV", ID: namespaceTwo.ID}})

	workerKVRequest(t, server, http.MethodPut, "/v1/workers/"+appOne.ID+"/kv/namespaces/"+namespaceOne.ID+"/color", []byte("blue"), http.StatusNoContent)
	workerKVRequest(t, server, http.MethodPut, "/v1/workers/"+appOne.ID+"/kv/namespaces/"+namespaceOne.ID+"/count", []byte("42"), http.StatusNoContent)
	if got := workerKVRequest(t, server, http.MethodGet, "/v1/workers/"+appOne.ID+"/kv/namespaces/"+namespaceOne.ID+"/color", nil, http.StatusOK); string(got) != "blue" {
		t.Fatalf("got %q, want app-one value", got)
	}
	var keys []nanoflare.WorkerKVKey
	requestJSON(t, server, http.MethodGet, "/v1/workers/"+appOne.ID+"/kv/namespaces/"+namespaceOne.ID, http.StatusOK, &keys)
	if len(keys) != 2 || keys[0].Key != "color" || keys[0].Size != 4 || keys[1].Key != "count" || keys[1].Size != 2 {
		t.Fatalf("unexpected KV keys: %#v", keys)
	}
	workerKVRequest(t, server, http.MethodGet, "/v1/workers/"+appTwo.ID+"/kv/namespaces/"+namespaceTwo.ID+"/color", nil, http.StatusNotFound)
	workerKVRequest(t, server, http.MethodDelete, "/v1/workers/"+appOne.ID+"/kv/namespaces/"+namespaceOne.ID+"/color", nil, http.StatusNoContent)
	workerKVRequest(t, server, http.MethodGet, "/v1/workers/"+appOne.ID+"/kv/namespaces/"+namespaceOne.ID+"/color", nil, http.StatusNotFound)
	workerKVRequest(t, server, http.MethodPut, "/v1/workers/missing/kv/namespaces/"+namespaceOne.ID+"/color", []byte("blue"), http.StatusNotFound)
	var metrics nanoflare.KVNamespaceMetrics
	requestJSON(t, server, http.MethodGet, "/v1/kv/namespaces/"+namespaceOne.ID+"/metrics", http.StatusOK, &metrics)
	if !metrics.Available || metrics.Reads != 0 || metrics.Writes != 0 {
		t.Fatalf("console KV routes should not count runtime metrics: %#v", metrics)
	}
}

func TestConsoleKVWriteOverOrgStorageLimitReturns402(t *testing.T) {
	dir := t.TempDir()
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	))
	server := NewServer(service)
	if err := store.CreateOrganization(nanoflare.Organization{ID: "org-console-kv-limit", Name: "Console KV Limit"}); err != nil {
		t.Fatal(err)
	}
	app, err := service.CreateApp(nanoflare.CreateAppInput{Name: "Console KV Limit", OrgID: "org-console-kv-limit", Hostname: "console-kv-limit.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{Name: "console-kv-limit", OrgID: "org-console-kv-limit"})
	if err != nil {
		t.Fatal(err)
	}
	deployWithKV(t, server, app.ID, []nanoflare.KVBinding{{Binding: "KV", ID: namespace.ID}})
	if err := store.AdjustKVNamespaceSize(namespace.ID, *nanoflare.OrgLimitsForLevel(nanoflare.UsageLevelDefault).KVStorageBytes); err != nil {
		t.Fatal(err)
	}

	workerKVRequest(t, server, http.MethodPut, "/v1/workers/"+app.ID+"/kv/namespaces/"+namespace.ID+"/over", []byte("x"), http.StatusPaymentRequired)
}

func TestRuntimeKVWriteOverOrgStorageLimitReturns402(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, discardWriter{})
	if err := store.CreateOrganization(nanoflare.Organization{ID: "org-runtime-kv-limit", Name: "Runtime KV Limit"}); err != nil {
		t.Fatal(err)
	}
	app, err := service.CreateApp(nanoflare.CreateAppInput{Name: "Runtime KV Limit", OrgID: "org-runtime-kv-limit", Hostname: "runtime-kv-limit.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{Name: "runtime-kv-limit", OrgID: "org-runtime-kv-limit"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AdjustKVNamespaceSize(namespace.ID, *nanoflare.OrgLimitsForLevel(nanoflare.UsageLevelDefault).KVStorageBytes); err != nil {
		t.Fatal(err)
	}

	kv := NewRuntimeKVServer(service)
	runtimeKVRequest(t, kv, http.MethodPut, "/over?urlencoded=true", app.RuntimeToken, namespace.ID, []byte("x"), http.StatusPaymentRequired)
}

func TestKVNamespaceAPIs(t *testing.T) {
	service := nanoflare.NewService(nanoflare.NewStore(), discardWriter{})
	server := NewServer(service)

	namespace := createKVNamespace(t, server, "shared-cache")
	var namespaces []nanoflare.KVNamespace
	requestJSON(t, server, http.MethodGet, "/v1/kv/namespaces", http.StatusOK, &namespaces)
	if len(namespaces) != 1 || namespaces[0].ID != namespace.ID || namespaces[0].Name != "shared-cache" {
		t.Fatalf("unexpected namespaces: %#v", namespaces)
	}

	var fetched nanoflare.KVNamespace
	requestJSON(t, server, http.MethodGet, "/v1/kv/namespaces/"+namespace.ID, http.StatusOK, &fetched)
	if fetched.ID != namespace.ID || fetched.Name != namespace.Name {
		t.Fatalf("unexpected namespace detail: %#v", fetched)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/v1/kv/namespaces/"+namespace.ID, strings.NewReader(`{"name":"shared-sessions"}`))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var updated nanoflare.KVNamespace
	if err := json.NewDecoder(recorder.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.ID != namespace.ID || updated.Name != "shared-sessions" {
		t.Fatalf("unexpected updated namespace: %#v", updated)
	}

	app := createApp(t, server, "Bound App", "bound.example.com")
	deployWithKV(t, server, app.ID, []nanoflare.KVBinding{{Binding: "CACHE", ID: namespace.ID}})

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodDelete, "/v1/kv/namespaces/"+namespace.ID, nil)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("delete status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestPrometheusMetricsExportsRuntimeAggregates(t *testing.T) {
	service := nanoflare.NewService(nanoflare.NewStore(), discardWriter{})
	server := NewServer(service)
	namespace := createKVNamespace(t, server, "metrics-cache")
	bucket, err := service.CreateObjectStorageBucket(nanoflare.CreateObjectStorageBucketInput{Name: "metrics-objects"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RecordRuntimeKVRead(namespace.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordRuntimeKVWrite(namespace.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordRuntimeObjectRead(bucket.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordRuntimeObjectWrite(bucket.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordRuntimeObjectWrite(bucket.ID); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`nanoflare_kv_reads_total{namespace_id="` + namespace.ID + `",namespace_name="metrics-cache"} 1`,
		`nanoflare_kv_writes_total{namespace_id="` + namespace.ID + `",namespace_name="metrics-cache"} 1`,
		`nanoflare_object_storage_reads_total{bucket_id="` + bucket.ID + `",bucket_name="metrics-objects"} 1`,
		`nanoflare_object_storage_writes_total{bucket_id="` + bucket.ID + `",bucket_name="metrics-objects"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
}

func TestDatabaseMetricsAPIAndPrometheusExport(t *testing.T) {
	service := nanoflare.NewService(nanoflare.NewStore(), discardWriter{})
	dbRuntime, err := database.NewSQLiteManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	service.SetDBExecutor(dbRuntime)
	server := NewServer(service)
	db, err := service.CreateDatabase(nanoflare.CreateDatabaseInput{Name: "metrics-db"})
	if err != nil {
		t.Fatal(err)
	}

	for _, sql := range []string{
		`CREATE TABLE papers (id integer primary key, title text)`,
		`INSERT INTO papers (title) VALUES ('hello')`,
		`SELECT * FROM papers`,
	} {
		requestJSONBytes(t, server, http.MethodPost, "/v1/db/"+db.ID+"/execute", []byte(`{"statements":[{"sql":`+strconv.Quote(sql)+`}]}`), http.StatusOK, &nanoflare.DBQueryResponse{})
	}

	var metrics nanoflare.DatabaseMetrics
	requestJSON(t, server, http.MethodGet, "/v1/db/"+db.ID+"/metrics", http.StatusOK, &metrics)
	if !metrics.Available || metrics.Queries != 3 || metrics.ReadQueries != 1 || metrics.WriteQueries != 2 {
		t.Fatalf("unexpected database query metrics: %#v", metrics)
	}
	if metrics.RowsRead != 1 || metrics.RowsReturned != 1 || metrics.RowsWritten != 1 {
		t.Fatalf("unexpected database row metrics: %#v", metrics)
	}
	if metrics.StorageBytes <= 0 || metrics.TableCount != 1 || metrics.P99DurationMS <= 0 {
		t.Fatalf("unexpected database runtime metrics: %#v", metrics)
	}

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`nanoflare_db_queries_total{database_id="` + db.ID + `",database_name="metrics-db"} 3`,
		`nanoflare_db_read_queries_total{database_id="` + db.ID + `",database_name="metrics-db"} 1`,
		`nanoflare_db_write_queries_total{database_id="` + db.ID + `",database_name="metrics-db"} 2`,
		`nanoflare_db_rows_read_total{database_id="` + db.ID + `",database_name="metrics-db"} 1`,
		`nanoflare_db_rows_written_total{database_id="` + db.ID + `",database_name="metrics-db"} 1`,
		`nanoflare_db_tables{database_id="` + db.ID + `",database_name="metrics-db"} 1`,
		`nanoflare_db_query_duration_seconds_count{database_id="` + db.ID + `",database_name="metrics-db"} 3`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
}

func TestDatabaseMetricsTimeseriesAPI(t *testing.T) {
	service := nanoflare.NewServiceWithConsole(nanoflare.NewStore(), discardWriter{}, nil, nil, fakeTraffic{})
	server := NewServer(service)
	db, err := service.CreateDatabase(nanoflare.CreateDatabaseInput{Name: "series-db"})
	if err != nil {
		t.Fatal(err)
	}

	var series nanoflare.DatabaseMetricsTimeseries
	requestJSON(t, server, http.MethodGet, "/v1/db/"+db.ID+"/metrics/timeseries", http.StatusOK, &series)
	if !series.Available || len(series.Queries) != 1 || series.Queries[0].Value != 7 {
		t.Fatalf("unexpected database series: %#v", series)
	}
}

func TestCreateAppGeneratesHostnameWhenOmitted(t *testing.T) {
	service := nanoflare.NewService(nanoflare.NewStore(), config.NewWriter(
		filepath.Join(t.TempDir(), "workerd.capnp"),
		filepath.Join(t.TempDir(), "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	))
	if err := service.SetBaseHostname("workers.example.com"); err != nil {
		t.Fatal(err)
	}
	server := NewServer(service)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/workers", strings.NewReader(`{"name":"Hello Worker"}`))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var app nanoflare.App
	if err := json.NewDecoder(recorder.Body).Decode(&app); err != nil {
		t.Fatal(err)
	}
	if app.Hostname != "hello-worker.workers.example.com" {
		t.Fatalf("generated hostname = %q", app.Hostname)
	}
}

func TestDeleteAppRemovesWorker(t *testing.T) {
	dir := t.TempDir()
	service := nanoflare.NewService(nanoflare.NewStore(), config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	))
	server := NewServer(service)
	app := createApp(t, server, "Delete App", "delete.example.com")
	deploy(t, server, app.ID)

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/v1/workers/"+app.ID, nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete app status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	requestJSON(t, server, http.MethodGet, "/v1/workers", http.StatusOK, &[]nanoflare.App{})
	requestJSON(t, server, http.MethodGet, "/v1/workers/"+app.ID, http.StatusNotFound, &map[string]string{})
}

func TestWorkerConsoleAPIsWithObjectBackedDeployment(t *testing.T) {
	dir := t.TempDir()
	store := &apiObjectBackedRepo{Store: nanoflare.NewStore()}
	objects := newAPIObjectStore()
	service := nanoflare.NewServiceWithConsole(store, config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	), objects, fakeOutput{}, fakeTraffic{})
	server := NewServer(service)
	app := createApp(t, server, "Object App", "object.example.com")
	bundle := `export default { async fetch() { return new Response("ok"); } }`
	deployContent(t, server, app.ID, []nanoflare.WorkerFile{{Path: "worker.js", Content: bundle}}, "")

	var detail nanoflare.WorkerDetail
	requestJSON(t, server, http.MethodGet, "/v1/workers/"+app.ID, http.StatusOK, &detail)
	if detail.Deployment == nil || detail.Deployment.BundleSize != int64(len(bundle)) {
		t.Fatalf("unexpected object-backed worker detail: %#v", detail)
	}

	var files []nanoflare.WorkerFile
	requestJSON(t, server, http.MethodGet, "/v1/workers/"+app.ID+"/files", http.StatusOK, &files)
	if len(files) != 1 || files[0].Content != bundle {
		t.Fatalf("unexpected object-backed worker files: %#v", files)
	}
	records, err := store.ListDeployments()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Deployment.ObjectKey == "" {
		t.Fatalf("expected object-backed deployment record, got %#v", records)
	}
	if _, ok := objects.objects[app.ID+":"+records[0].Deployment.ObjectKey]; !ok {
		t.Fatalf("expected uploaded deployment object for %s", records[0].Deployment.ObjectKey)
	}
}

func TestWorkerConsoleAPIsForRegisteredWorkerWithoutDeployment(t *testing.T) {
	service := nanoflare.NewService(nanoflare.NewStore(), config.NewWriter(
		filepath.Join(t.TempDir(), "workerd.capnp"),
		filepath.Join(t.TempDir(), "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	))
	server := NewServer(service)
	app := createApp(t, server, "Draft App", "draft.example.com")

	var detail nanoflare.WorkerDetail
	requestJSON(t, server, http.MethodGet, "/v1/workers/"+app.ID, http.StatusOK, &detail)
	if detail.Deployment != nil {
		t.Fatalf("unexpected deployment: %#v", detail.Deployment)
	}
	var files []nanoflare.WorkerFile
	requestJSON(t, server, http.MethodGet, "/v1/workers/"+app.ID+"/files", http.StatusOK, &files)
	if len(files) != 0 {
		t.Fatalf("unexpected files: %#v", files)
	}
	requestJSON(t, server, http.MethodGet, "/v1/workers/missing", http.StatusNotFound, &map[string]string{})
}

func TestListWorkerDeploymentsIncludesInactiveRecords(t *testing.T) {
	dir := t.TempDir()
	service := nanoflare.NewService(nanoflare.NewStore(), config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	))
	server := NewServer(service)
	app := createApp(t, server, "Ledger App", "ledger.example.com")
	first := deployContent(t, server, app.ID, []nanoflare.WorkerFile{{Path: "first.js", Content: "first"}}, "")
	second := deployContent(t, server, app.ID, []nanoflare.WorkerFile{{Path: "second.js", Content: "second"}}, "")

	var deployments []nanoflare.ConsoleDeployment
	requestJSON(t, server, http.MethodGet, "/v1/workers/"+app.ID+"/deployments", http.StatusOK, &deployments)
	if len(deployments) != 2 {
		t.Fatalf("deployments = %#v, want two records", deployments)
	}
	states := map[string]string{}
	for _, deployment := range deployments {
		states[deployment.ID] = deployment.State
	}
	if states[first.ID] != "inactive" || states[second.ID] != "active" {
		t.Fatalf("deployment states = %#v, want first inactive and second active", states)
	}
	requestJSON(t, server, http.MethodGet, "/v1/workers/missing/deployments", http.StatusNotFound, &map[string]string{})
}

func TestSetWorkerDeploymentTraffic(t *testing.T) {
	dir := t.TempDir()
	service := nanoflare.NewService(nanoflare.NewStore(), config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	))
	server := NewServer(service)
	app := createApp(t, server, "Ledger App", "ledger.example.com")
	first := deployContent(t, server, app.ID, []nanoflare.WorkerFile{{Path: "first.js", Content: "first"}}, "")
	second := deployContent(t, server, app.ID, []nanoflare.WorkerFile{{Path: "second.js", Content: "second"}}, "")

	body := []byte(`{"deployments":[{"id":"` + first.ID + `","traffic_percent":10},{"id":"` + second.ID + `","traffic_percent":90}]}`)
	var deployments []nanoflare.ConsoleDeployment
	requestJSONBytes(t, server, http.MethodPut, "/v1/workers/"+app.ID+"/deployments/traffic", body, http.StatusOK, &deployments)
	traffic := map[string]int{}
	for _, deployment := range deployments {
		traffic[deployment.ID] = deployment.TrafficPercent
	}
	if traffic[first.ID] != 10 || traffic[second.ID] != 90 {
		t.Fatalf("traffic = %#v, want 10/90", traffic)
	}

	var detail nanoflare.WorkerDetail
	requestJSON(t, server, http.MethodGet, "/v1/workers/"+app.ID, http.StatusOK, &detail)
	if detail.Deployment == nil || detail.Deployment.ID != second.ID || detail.Deployment.TrafficPercent != 90 {
		t.Fatalf("detail deployment = %#v, want highest traffic deployment", detail.Deployment)
	}

	requestJSONBytes(t, server, http.MethodPut, "/v1/workers/"+app.ID+"/deployments/traffic", []byte(`{"deployments":[{"id":"`+first.ID+`","traffic_percent":50}]}`), http.StatusBadRequest, &map[string]string{})
	requestJSONBytes(t, server, http.MethodPut, "/v1/workers/"+app.ID+"/deployments/traffic", []byte(`{"deployments":[{"id":"`+first.ID+`","traffic_percent":50},{"id":"`+first.ID+`","traffic_percent":50}]}`), http.StatusBadRequest, &map[string]string{})
	requestJSONBytes(t, server, http.MethodPut, "/v1/workers/"+app.ID+"/deployments/traffic", []byte(`{"deployments":[{"id":"missing","traffic_percent":100}]}`), http.StatusBadRequest, &map[string]string{})
}

func TestTraefikConfigRequiresToken(t *testing.T) {
	service := nanoflare.NewService(nanoflare.NewStore(), config.NewWriter(
		filepath.Join(t.TempDir(), "workerd.capnp"),
		filepath.Join(t.TempDir(), "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	))
	server := NewServerWithTraefik(service, staticTraefikConfig("http:\n  routers: {}\n"), "secret")

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/internal/traefik/config", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}

	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/internal/traefik/config", nil)
	request.Header.Set("Authorization", "Bearer secret")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "http:\n  routers: {}\n" {
		t.Fatalf("authenticated response status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/yaml" {
		t.Fatalf("content type = %q, want application/yaml", got)
	}
}

func TestAppGatewayServesAttachedAsset(t *testing.T) {
	dir := t.TempDir()
	store := newAPIObjectBackedRepo()
	objects := newAPIObjectStore()
	service := nanoflare.NewServiceWithObjects(store, config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	), objects)
	server := NewServer(service)
	app := createApp(t, server, "Assets", "assets.example.com")
	deployWithAssets(t, server, app.ID,
		[]nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() { return new Response("worker"); } }`}},
		[]nanoflare.AssetFile{{Path: "index.html", ContentType: "text/html; charset=utf-8", Data: []byte("<h1>Site</h1>")}},
	)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/internal/http/workers/"+app.ID+"/", nil)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "<h1>Site</h1>" {
		t.Fatalf("gateway status = %d body = %q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("content type = %q", got)
	}
}

func TestRuntimeAssetServerServesAttachedAsset(t *testing.T) {
	dir := t.TempDir()
	store := newAPIObjectBackedRepo()
	objects := newAPIObjectStore()
	service := nanoflare.NewServiceWithObjects(store, config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	), objects)
	server := NewServer(service)
	app := createApp(t, server, "Assets", "assets.example.com")
	deployment := deployWithAssets(t, server, app.ID,
		[]nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() { return new Response("worker"); } }`}},
		[]nanoflare.AssetFile{{Path: "logo.svg", ContentType: "image/svg+xml", Data: []byte("<svg />")}},
	)
	if len(deployment.Assets) != 1 {
		t.Fatalf("deployment assets = %#v", deployment.Assets)
	}

	runtimeAssets := NewRuntimeAssetServer(service)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/internal/runtime/assets/logo.svg", nil)
	request.Header.Set("Authorization", "Bearer "+runtimeTokens(t, store)[app.ID])
	runtimeAssets.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "<svg />" {
		t.Fatalf("runtime asset status = %d body = %q", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/logo.svg?v=1", nil)
	request.Header.Set("Authorization", "Bearer "+runtimeTokens(t, store)[app.ID])
	runtimeAssets.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "<svg />" {
		t.Fatalf("runtime direct asset status = %d body = %q", recorder.Code, recorder.Body.String())
	}
}

func TestRuntimeObjectServerSupportsCoreOperations(t *testing.T) {
	dir := t.TempDir()
	store := newAPIObjectBackedRepo()
	objects := newAPIObjectStore()
	service := nanoflare.NewServiceWithObjects(store, config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	), objects)
	server := NewServer(service)
	app := createApp(t, server, "Objects", "objects.example.com")
	bucket, err := service.CreateObjectStorageBucket(nanoflare.CreateObjectStorageBucketInput{Name: "objects"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deploy(app.ID, nanoflare.DeployInput{
		Files:                []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { async fetch() { return new Response("ok"); } };`}},
		Entrypoint:           "worker.js",
		Format:               "modules",
		CompatibilityDate:    "2025-12-10",
		ObjectStorageBuckets: []nanoflare.ObjectStorageBucketBinding{{Binding: "OBJECTS", BucketID: bucket.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	token := runtimeTokens(t, store)[app.ID]

	request := httptest.NewRequest(http.MethodPut, "/internal/runtime/objects/folder%2Fhello.txt", bytes.NewReader([]byte("hello")))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Nanoflare-Object-Bucket-ID", bucket.ID)
	request.Header.Set("Content-Type", "text/plain")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("put status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var object nanoflare.ObjectInfo
	if err := json.NewDecoder(recorder.Body).Decode(&object); err != nil {
		t.Fatal(err)
	}
	if object.Key != "folder/hello.txt" || object.Size != 5 || object.HTTPMetadata.ContentType != "text/plain" {
		t.Fatalf("unexpected object info: %#v", object)
	}
	var metrics nanoflare.ObjectStorageBucketMetrics
	requestJSON(t, server, http.MethodGet, "/v1/object-storage-buckets/"+bucket.ID+"/metrics", http.StatusOK, &metrics)
	if !metrics.Available || metrics.Reads != 0 || metrics.Writes != 1 || metrics.Size != 5 {
		t.Fatalf("unexpected object metrics after put: %#v", metrics)
	}

	request = httptest.NewRequest(http.MethodHead, "/internal/runtime/objects/folder%2Fhello.txt", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Nanoflare-Object-Bucket-ID", bucket.ID)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("head status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Nanoflare-Object-Key"); got != "folder/hello.txt" {
		t.Fatalf("head key = %q", got)
	}

	request = httptest.NewRequest(http.MethodGet, "/internal/runtime/objects/folder%2Fhello.txt", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Nanoflare-Object-Bucket-ID", bucket.ID)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "hello" {
		t.Fatalf("get status = %d body = %q", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodDelete, "/internal/runtime/objects/folder%2Fhello.txt", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Nanoflare-Object-Bucket-ID", bucket.ID)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	requestJSON(t, server, http.MethodGet, "/v1/object-storage-buckets/"+bucket.ID+"/metrics", http.StatusOK, &metrics)
	if !metrics.Available || metrics.Reads != 2 || metrics.Writes != 2 || metrics.Size != 0 {
		t.Fatalf("unexpected object metrics after runtime operations: %#v", metrics)
	}

	request = httptest.NewRequest(http.MethodGet, "/internal/runtime/objects/folder%2Fhello.txt", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Nanoflare-Object-Bucket-ID", bucket.ID)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing get status = %d", recorder.Code)
	}
}

func TestRuntimeObjectWriteOverOrgStorageLimitReturns402(t *testing.T) {
	dir := t.TempDir()
	store := newAPIObjectBackedRepo()
	objects := newAPIObjectStore()
	service := nanoflare.NewServiceWithObjects(store, config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	), objects)
	server := NewServer(service)
	if err := store.CreateOrganization(nanoflare.Organization{ID: "org-runtime-object-limit", Name: "Runtime Object Limit"}); err != nil {
		t.Fatal(err)
	}
	app, err := service.CreateApp(nanoflare.CreateAppInput{Name: "Runtime Object Limit", OrgID: "org-runtime-object-limit", Hostname: "runtime-object-limit.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	bucket, err := service.CreateObjectStorageBucket(nanoflare.CreateObjectStorageBucketInput{Name: "runtime-object-limit", OrgID: "org-runtime-object-limit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deploy(app.ID, nanoflare.DeployInput{
		Files:                []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { async fetch() { return new Response("ok"); } };`}},
		Entrypoint:           "worker.js",
		Format:               "modules",
		CompatibilityDate:    "2025-12-10",
		ObjectStorageBuckets: []nanoflare.ObjectStorageBucketBinding{{Binding: "OBJECTS", BucketID: bucket.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AdjustObjectStorageBucketSize(bucket.ID, *nanoflare.OrgLimitsForLevel(nanoflare.UsageLevelDefault).ObjectStorageBytes); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPut, "/internal/runtime/objects/over.txt", bytes.NewReader([]byte("x")))
	request.Header.Set("Authorization", "Bearer "+app.RuntimeToken)
	request.Header.Set("X-Nanoflare-Object-Bucket-ID", bucket.ID)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusPaymentRequired {
		t.Fatalf("runtime object over limit status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestConsoleObjectWriteOverOrgStorageLimitReturns402(t *testing.T) {
	dir := t.TempDir()
	store := newAPIObjectBackedRepo()
	objects := newAPIObjectStore()
	service := nanoflare.NewServiceWithObjects(store, config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	), objects)
	server := NewServer(service)
	if err := store.CreateOrganization(nanoflare.Organization{ID: "org-console-object-limit", Name: "Console Object Limit"}); err != nil {
		t.Fatal(err)
	}
	app, err := service.CreateApp(nanoflare.CreateAppInput{Name: "Console Object Limit", OrgID: "org-console-object-limit", Hostname: "console-object-limit.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	bucket, err := service.CreateObjectStorageBucket(nanoflare.CreateObjectStorageBucketInput{Name: "console-object-limit", OrgID: "org-console-object-limit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deploy(app.ID, nanoflare.DeployInput{
		Files:                []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { async fetch() { return new Response("ok"); } };`}},
		Entrypoint:           "worker.js",
		Format:               "modules",
		CompatibilityDate:    "2025-12-10",
		ObjectStorageBuckets: []nanoflare.ObjectStorageBucketBinding{{Binding: "OBJECTS", BucketID: bucket.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AdjustObjectStorageBucketSize(bucket.ID, *nanoflare.OrgLimitsForLevel(nanoflare.UsageLevelDefault).ObjectStorageBytes); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPut, "/v1/workers/"+app.ID+"/object-storage-buckets/"+bucket.ID+"/over.txt", bytes.NewReader([]byte("x")))
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusPaymentRequired {
		t.Fatalf("console object over limit status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestObjectStorageMetricsReconcileExistingObjects(t *testing.T) {
	dir := t.TempDir()
	store := newAPIObjectBackedRepo()
	objects := newAPIObjectStore()
	service := nanoflare.NewServiceWithObjects(store, config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	), objects)
	server := NewServer(service)
	app := createApp(t, server, "Existing Objects", "existing-objects.example.com")
	bucket, err := service.CreateObjectStorageBucket(nanoflare.CreateObjectStorageBucketInput{Name: "existing-objects"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deploy(app.ID, nanoflare.DeployInput{
		Files:                []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { async fetch() { return new Response("ok"); } };`}},
		Entrypoint:           "worker.js",
		Format:               "modules",
		CompatibilityDate:    "2025-12-10",
		ObjectStorageBuckets: []nanoflare.ObjectStorageBucketBinding{{Binding: "OBJECTS", BucketID: bucket.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := objects.Put("object-storage-buckets", "buckets/"+bucket.ID+"/legacy.txt", "text/plain", []byte("legacy")); err != nil {
		t.Fatal(err)
	}
	if err := store.AdjustObjectStorageBucketSize(bucket.ID, 123); err != nil {
		t.Fatal(err)
	}

	var metrics nanoflare.ObjectStorageBucketMetrics
	requestJSON(t, server, http.MethodGet, "/v1/object-storage-buckets/"+bucket.ID+"/metrics", http.StatusOK, &metrics)
	if !metrics.Available || metrics.Size != 6 {
		t.Fatalf("unexpected reconciled object metrics: %#v", metrics)
	}
}

func TestGatewayFallsBackToWorkerWhenAssetMissing(t *testing.T) {
	dir := t.TempDir()
	store := newAPIObjectBackedRepo()
	objects := newAPIObjectStore()
	service := nanoflare.NewServiceWithObjects(store, config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	), objects)
	server := NewServer(service)
	app := createApp(t, server, "Assets", "assets.example.com")
	deployWithAssets(t, server, app.ID,
		[]nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() { return new Response("worker"); } }`}},
		[]nanoflare.AssetFile{{Path: "index.html", ContentType: "text/html; charset=utf-8", Data: []byte("<h1>Site</h1>")}},
	)
	workerListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portValue, err := net.SplitHostPort(workerListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	workerPort, err := strconv.Atoi(portValue)
	if err != nil {
		t.Fatal(err)
	}
	workerServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("worker"))
	})}
	go func() {
		_ = workerServer.Serve(workerListener)
	}()
	t.Cleanup(func() {
		_ = workerServer.Close()
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/internal/http/workers/"+app.ID+"/"+strconv.Itoa(workerPort)+"/missing", nil)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "worker" {
		t.Fatalf("gateway fallback status = %d body = %q", recorder.Code, recorder.Body.String())
	}
}

func TestGatewayRunWorkerFirstTrueProxiesBeforeAssets(t *testing.T) {
	dir := t.TempDir()
	store := newAPIObjectBackedRepo()
	objects := newAPIObjectStore()
	service := nanoflare.NewServiceWithObjects(store, config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	), objects)
	server := NewServer(service)
	app := createApp(t, server, "Assets", "assets.example.com")
	deployWithAssetsConfig(t, server, app.ID,
		[]nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() { return new Response("worker"); } }`}},
		[]nanoflare.AssetFile{{Path: "index.html", ContentType: "text/html; charset=utf-8", Data: []byte("<h1>Site</h1>")}},
		nanoflare.AssetConfig{RunWorkerFirst: runWorkerFirstTrue(t)},
	)
	workerPort := startTestWorker(t, "worker")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/internal/http/workers/"+app.ID+"/"+strconv.Itoa(workerPort)+"/", nil)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "worker" {
		t.Fatalf("gateway status = %d body = %q", recorder.Code, recorder.Body.String())
	}
}

func TestGatewayUsesStickyDeploymentCookie(t *testing.T) {
	dir := t.TempDir()
	store := newAPIObjectBackedRepo()
	objects := newAPIObjectStore()
	service := nanoflare.NewServiceWithObjects(store, config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	), objects)
	server := NewServer(service)
	app := createApp(t, server, "Assets", "assets.example.com")
	first := deployWithAssets(t, server, app.ID,
		[]nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() { return new Response("first"); } }`}},
		[]nanoflare.AssetFile{{Path: "index.html", ContentType: "text/html; charset=utf-8", Data: []byte("first")}},
	)
	second := deployWithAssets(t, server, app.ID,
		[]nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() { return new Response("second"); } }`}},
		[]nanoflare.AssetFile{{Path: "index.html", ContentType: "text/html; charset=utf-8", Data: []byte("second")}},
	)
	body := []byte(`{"deployments":[{"id":"` + first.ID + `","traffic_percent":50},{"id":"` + second.ID + `","traffic_percent":50}]}`)
	requestJSONBytes(t, server, http.MethodPut, "/v1/workers/"+app.ID+"/deployments/traffic", body, http.StatusOK, &[]nanoflare.ConsoleDeployment{})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/internal/http/workers/"+app.ID+"/", nil)
	request.AddCookie(&http.Cookie{Name: "nf_deployment_" + app.ID, Value: first.ID})
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "first" {
		t.Fatalf("gateway sticky response status = %d body = %q", recorder.Code, recorder.Body.String())
	}
	if cookies := recorder.Result().Cookies(); len(cookies) == 0 || cookies[0].Value != first.ID {
		t.Fatalf("sticky cookie not refreshed: %#v", cookies)
	}
}

func TestGatewayAssetHitDoesNotEnsureLazyRuntime(t *testing.T) {
	dir := t.TempDir()
	store := newAPIObjectBackedRepo()
	objects := newAPIObjectStore()
	service := nanoflare.NewServiceWithObjects(store, config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	), objects)
	lazy := &recordingRuntime{}
	server := NewServerWithRuntime(service, nil, "", nil, nil, lazy)
	app := createApp(t, server, "Assets", "assets.example.com")
	deployWithAssets(t, server, app.ID,
		[]nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() { return new Response("worker"); } }`}},
		[]nanoflare.AssetFile{{Path: "index.html", ContentType: "text/html; charset=utf-8", Data: []byte("<h1>Site</h1>")}},
	)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/internal/http/workers/"+app.ID+"/", nil)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "<h1>Site</h1>" {
		t.Fatalf("gateway asset status = %d body = %q", recorder.Code, recorder.Body.String())
	}
	if lazy.ensureCount != 0 {
		t.Fatalf("runtime ensures = %d, want 0 for direct asset hit", lazy.ensureCount)
	}
}

func TestGatewayAssetMissEnsuresLazyRuntime(t *testing.T) {
	dir := t.TempDir()
	store := newAPIObjectBackedRepo()
	objects := newAPIObjectStore()
	service := nanoflare.NewServiceWithObjects(store, config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	), objects)
	lazy := &recordingRuntime{port: startTestWorker(t, "worker")}
	server := NewServerWithRuntime(service, nil, "", nil, nil, lazy)
	app := createApp(t, server, "Assets", "assets.example.com")
	deployWithAssets(t, server, app.ID,
		[]nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() { return new Response("worker"); } }`}},
		[]nanoflare.AssetFile{{Path: "index.html", ContentType: "text/html; charset=utf-8", Data: []byte("<h1>Site</h1>")}},
	)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/internal/http/workers/"+app.ID+"/missing", nil)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "worker" {
		t.Fatalf("gateway worker status = %d body = %q", recorder.Code, recorder.Body.String())
	}
	if lazy.ensureCount != 1 || lazy.releaseCount != 1 {
		t.Fatalf("runtime ensure/release = %d/%d, want 1/1", lazy.ensureCount, lazy.releaseCount)
	}
}

func TestGatewayRunWorkerFirstRoutesOnlyMatchingPaths(t *testing.T) {
	dir := t.TempDir()
	store := newAPIObjectBackedRepo()
	objects := newAPIObjectStore()
	service := nanoflare.NewServiceWithObjects(store, config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	), objects)
	server := NewServer(service)
	app := createApp(t, server, "Assets", "assets.example.com")
	deployWithAssetsConfig(t, server, app.ID,
		[]nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() { return new Response("worker"); } }`}},
		[]nanoflare.AssetFile{{Path: "index.html", ContentType: "text/html; charset=utf-8", Data: []byte("<h1>Site</h1>")}},
		nanoflare.AssetConfig{
			NotFoundHandling: "single-page-application",
			RunWorkerFirst:   nanoflare.RunWorkerFirst{"/api/*"},
		},
	)
	workerPort := startTestWorker(t, "worker")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/internal/http/workers/"+app.ID+"/"+strconv.Itoa(workerPort)+"/", nil)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "<h1>Site</h1>" {
		t.Fatalf("gateway asset status = %d body = %q", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/internal/http/workers/"+app.ID+"/"+strconv.Itoa(workerPort)+"/api/visits", nil)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "worker" {
		t.Fatalf("gateway worker status = %d body = %q", recorder.Code, recorder.Body.String())
	}
}

func TestGatewayRunWorkerFirstNegativeRouteServesAsset(t *testing.T) {
	dir := t.TempDir()
	store := newAPIObjectBackedRepo()
	objects := newAPIObjectStore()
	service := nanoflare.NewServiceWithObjects(store, config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	), objects)
	server := NewServer(service)
	app := createApp(t, server, "Assets", "assets.example.com")
	deployWithAssetsConfig(t, server, app.ID,
		[]nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() { return new Response("worker"); } }`}},
		[]nanoflare.AssetFile{
			{Path: "index.html", ContentType: "text/html; charset=utf-8", Data: []byte("<h1>Site</h1>")},
			{Path: "api/docs/index.html", ContentType: "text/html; charset=utf-8", Data: []byte("<h1>Docs</h1>")},
		},
		nanoflare.AssetConfig{
			NotFoundHandling: "single-page-application",
			RunWorkerFirst:   nanoflare.RunWorkerFirst{"/api/*", "!/api/docs/*"},
		},
	)
	workerPort := startTestWorker(t, "worker")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/internal/http/workers/"+app.ID+"/"+strconv.Itoa(workerPort)+"/api/docs/", nil)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "<h1>Docs</h1>" {
		t.Fatalf("gateway status = %d body = %q", recorder.Code, recorder.Body.String())
	}
}

type staticTraefikConfig string

func (config staticTraefikConfig) TraefikConfig() []byte {
	return []byte(config)
}

type recordingRuntime struct {
	port         int
	ensureCount  int
	releaseCount int
}

func (r *recordingRuntime) Ensure(context.Context, nanoflare.ActiveDeployment) (runtime.EnsuredWorker, error) {
	r.ensureCount++
	return runtime.EnsuredWorker{
		Port: r.port,
		Release: func() {
			r.releaseCount++
		},
	}, nil
}

func createApp(t *testing.T, server http.Handler, name, hostname string) nanoflare.App {
	t.Helper()
	body := `{"name":"` + name + `","hostname":"` + hostname + `"}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/workers", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create app status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var app nanoflare.App
	if err := json.NewDecoder(recorder.Body).Decode(&app); err != nil {
		t.Fatal(err)
	}
	if len(app.ID) != 48 {
		t.Fatalf("generated app id = %q, want 48 character token", app.ID)
	}
	return app
}

func createKVNamespace(t *testing.T, server http.Handler, name string) nanoflare.KVNamespace {
	t.Helper()
	body := `{"name":"` + name + `"}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/kv/namespaces", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create kv namespace status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var namespace nanoflare.KVNamespace
	if err := json.NewDecoder(recorder.Body).Decode(&namespace); err != nil {
		t.Fatal(err)
	}
	return namespace
}

func deploy(t *testing.T, server http.Handler, appID string) nanoflare.Deployment {
	t.Helper()
	return deployContent(t, server, appID, []nanoflare.WorkerFile{{Path: "worker.js", Content: `addEventListener("fetch", () => {});`}}, "")
}

func deployWithKV(t *testing.T, server http.Handler, appID string, kvNamespaces []nanoflare.KVBinding) nanoflare.Deployment {
	t.Helper()
	body, err := json.Marshal(nanoflare.DeployInput{
		Files:             []nanoflare.WorkerFile{{Path: "worker.js", Content: `addEventListener("fetch", () => {});`}},
		CompatibilityDate: "2025-12-10",
		KVNamespaces:      kvNamespaces,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/workers/"+appID+"/deployments", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("deploy status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var deployment nanoflare.Deployment
	if err := json.NewDecoder(recorder.Body).Decode(&deployment); err != nil {
		t.Fatal(err)
	}
	return deployment
}

func deployContent(t *testing.T, server http.Handler, appID string, files []nanoflare.WorkerFile, entrypoint string) nanoflare.Deployment {
	t.Helper()
	body, err := json.Marshal(nanoflare.DeployInput{Files: files, Entrypoint: entrypoint, CompatibilityDate: "2025-12-10"})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/workers/"+appID+"/deployments", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("deploy status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var deployment nanoflare.Deployment
	if err := json.NewDecoder(recorder.Body).Decode(&deployment); err != nil {
		t.Fatal(err)
	}
	if len(deployment.ID) != 48 {
		t.Fatalf("generated deployment id = %q, want 48 character token", deployment.ID)
	}
	return deployment
}

func deployWithAssets(t *testing.T, server http.Handler, appID string, files []nanoflare.WorkerFile, assets []nanoflare.AssetFile) nanoflare.Deployment {
	t.Helper()
	return deployWithAssetsConfig(t, server, appID, files, assets, nanoflare.AssetConfig{})
}

func deployWithAssetsConfig(t *testing.T, server http.Handler, appID string, files []nanoflare.WorkerFile, assets []nanoflare.AssetFile, assetConfig nanoflare.AssetConfig) nanoflare.Deployment {
	t.Helper()
	body, err := json.Marshal(nanoflare.DeployInput{
		Files:             files,
		Assets:            assets,
		CompatibilityDate: "2025-12-10",
		AssetConfig:       assetConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/workers/"+appID+"/deployments", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("deploy status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var deployment nanoflare.Deployment
	if err := json.NewDecoder(recorder.Body).Decode(&deployment); err != nil {
		t.Fatal(err)
	}
	return deployment
}

func runWorkerFirstTrue(t *testing.T) nanoflare.RunWorkerFirst {
	t.Helper()
	var runWorkerFirst nanoflare.RunWorkerFirst
	if err := json.Unmarshal([]byte("true"), &runWorkerFirst); err != nil {
		t.Fatal(err)
	}
	return runWorkerFirst
}

func startTestWorker(t *testing.T, body string) int {
	t.Helper()
	workerListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portValue, err := net.SplitHostPort(workerListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	workerPort, err := strconv.Atoi(portValue)
	if err != nil {
		t.Fatal(err)
	}
	workerServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})}
	go func() {
		_ = workerServer.Serve(workerListener)
	}()
	t.Cleanup(func() {
		_ = workerServer.Close()
	})
	return workerPort
}

func requestJSON(t *testing.T, server http.Handler, method, path string, wantStatus int, target any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s status = %d, body = %s", method, path, recorder.Code, recorder.Body.String())
	}
	if err := json.NewDecoder(recorder.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func requestJSONBytes(t *testing.T, server http.Handler, method, path string, body []byte, wantStatus int, target any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s status = %d, body = %s", method, path, recorder.Code, recorder.Body.String())
	}
	if target != nil {
		if err := json.NewDecoder(recorder.Body).Decode(target); err != nil {
			t.Fatal(err)
		}
	}
}

type fakeTraffic struct{}

type fakeOutput struct{}

func (fakeOutput) Output(string) []nanoflare.WorkerOutputLine {
	return nil
}

func (fakeTraffic) Traffic(string) (nanoflare.WorkerTraffic, error) {
	return nanoflare.WorkerTraffic{
		Available:         true,
		RequestsPerSecond: 4.25,
		Traffic:           []float64{3, 4},
		WorkerTrafficDuration: nanoflare.WorkerTrafficDuration{
			DurationMsAvg:       12.5,
			DurationMsP95:       20,
			DurationMsPerSecond: 1.5,
			DurationSeries:      []float64{1.25, 1.75},
		},
	}, nil
}

func (fakeTraffic) DatabaseMetricsTimeseries(string) (nanoflare.DatabaseMetricsTimeseries, error) {
	return nanoflare.DatabaseMetricsTimeseries{
		Available: true,
		Queries: []nanoflare.MetricPoint{{
			Timestamp: time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC),
			Value:     7,
		}},
	}, nil
}

type apiObjectStore struct {
	objects map[string]nanoflare.ObjectBody
}

func newAPIObjectStore() *apiObjectStore {
	return &apiObjectStore{objects: make(map[string]nanoflare.ObjectBody)}
}

func (s *apiObjectStore) PresignUpload(string, string, time.Duration) (string, error) {
	return "", nil
}

func (s *apiObjectStore) PresignDownload(string, string, time.Duration) (string, error) {
	return "", nil
}

func (s *apiObjectStore) Put(appID, path string, contentType string, data []byte) (nanoflare.ObjectInfo, error) {
	visiblePath := path
	if strings.HasPrefix(path, "buckets/") {
		parts := strings.SplitN(path, "/", 3)
		if len(parts) == 3 {
			visiblePath = parts[2]
		}
	}
	object := nanoflare.ObjectBody{
		ObjectInfo: nanoflare.ObjectInfo{
			Key:      path,
			Size:     int64(len(data)),
			ETag:     "etag-" + visiblePath,
			HTTPETag: `"etag-` + visiblePath + `"`,
			Uploaded: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			HTTPMetadata: nanoflare.ObjectHTTPMetadata{
				ContentType: contentType,
			},
		},
		Body: append([]byte(nil), data...),
	}
	s.objects[appID+":"+path] = object
	return object.ObjectInfo, nil
}

func (s *apiObjectStore) Get(appID, path string) (nanoflare.ObjectBody, error) {
	data, ok := s.objects[appID+":"+path]
	if !ok {
		return nanoflare.ObjectBody{}, nanoflare.ErrObjectNotFound
	}
	data.Body = append([]byte(nil), data.Body...)
	return data, nil
}

func (s *apiObjectStore) Head(appID, path string) (nanoflare.ObjectInfo, error) {
	data, ok := s.objects[appID+":"+path]
	if !ok {
		return nanoflare.ObjectInfo{}, nanoflare.ErrObjectNotFound
	}
	return data.ObjectInfo, nil
}

func (s *apiObjectStore) List(appID, prefix string) ([]nanoflare.ObjectInfo, error) {
	items := make([]nanoflare.ObjectInfo, 0)
	for key, data := range s.objects {
		if !strings.HasPrefix(key, appID+":"+prefix+"/") {
			continue
		}
		object := data.ObjectInfo
		object.Key = strings.TrimPrefix(object.Key, prefix+"/")
		items = append(items, object)
	}
	return items, nil
}

func (s *apiObjectStore) Delete(appID, path string) error {
	delete(s.objects, appID+":"+path)
	return nil
}

type apiObjectBackedRepo struct {
	*nanoflare.Store
}

func newAPIObjectBackedRepo() *apiObjectBackedRepo {
	return &apiObjectBackedRepo{Store: nanoflare.NewStore()}
}

func (r *apiObjectBackedRepo) Activate(deployment nanoflare.Deployment) error {
	copy := deployment
	if copy.ObjectKey != "" {
		copy.Files = nil
	}
	return r.Store.Activate(copy)
}

func (r *apiObjectBackedRepo) ActiveDeployments() ([]nanoflare.ActiveDeployment, error) {
	active, err := r.Store.ActiveDeployments()
	if err != nil {
		return nil, err
	}
	for i := range active {
		if active[i].Deployment.ObjectKey != "" {
			active[i].Deployment.Files = nil
		}
	}
	return active, nil
}

func request(t *testing.T, server http.Handler, method, path, capability, body string, wantStatus int) map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	if capability != "" {
		request.Header.Set("Authorization", "Bearer "+capability)
	}
	server.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s status = %d, body = %s", method, path, recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() == 0 {
		return nil
	}
	var response map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func runtimeTokens(t *testing.T, store nanoflare.Repository) map[string]string {
	t.Helper()
	apps, err := store.ListApps()
	if err != nil {
		t.Fatal(err)
	}
	tokens := make(map[string]string, len(apps))
	for _, app := range apps {
		tokens[app.ID] = app.RuntimeToken
	}
	return tokens
}

func runtimeKVRequest(t *testing.T, server http.Handler, method, path, token, namespaceID string, body []byte, wantStatus int) []byte {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if namespaceID != "" {
		request.Header.Set("X-Nanoflare-KV-Namespace-ID", namespaceID)
	}
	server.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s status = %d, body = %s", method, path, recorder.Code, recorder.Body.String())
	}
	return recorder.Body.Bytes()
}

func workerKVRequest(t *testing.T, server http.Handler, method, path string, body []byte, wantStatus int) []byte {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	server.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s status = %d, body = %s", method, path, recorder.Code, recorder.Body.String())
	}
	return recorder.Body.Bytes()
}
