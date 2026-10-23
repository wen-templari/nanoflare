package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type dynamicUnitProcess struct{}

type dynamicUnitTransport func(*http.Request) (*http.Response, error)

func (f dynamicUnitTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (dynamicUnitProcess) Done() <-chan error         { return make(chan error) }
func (dynamicUnitProcess) Stop(context.Context) error { return nil }

type dynamicControlFixture struct {
	mu       sync.Mutex
	next     int
	pins     map[string]int
	requests []dynamicForwardedRequest
}
type dynamicForwardedRequest struct {
	path, query string
	headers     http.Header
	body        []byte
}

func newDynamicControlFixture(t *testing.T) (*DynamicManager, *dynamicControlFixture, nanoflare.ActiveDeployment, string) {
	t.Helper()
	state := &dynamicControlFixture{pins: map[string]int{}}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/runtime/executions/") {
			if r.Header.Get("X-Nanoflare-Runner-Token") != "control-secret" {
				http.Error(w, "unauthorized control", 403)
				return
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			state.mu.Lock()
			defer state.mu.Unlock()
			switch strings.TrimPrefix(r.URL.Path, "/internal/runtime/executions/") {
			case "admit":
				if body["session_id"] != "unit-session" {
					http.Error(w, "missing session", 403)
					return
				}
				state.next++
				pin := body["session_id"] + ":" + body["admission_id"]
				state.pins[pin] = 0
				_ = json.NewEncoder(w).Encode(map[string]string{"token": fmt.Sprintf("token-%d", state.next), "pin_id": pin})
			case "release":
				state.pins[body["pin_id"]]++
			default:
			}
			return
		}
		body, _ := io.ReadAll(r.Body)
		state.mu.Lock()
		state.requests = append(state.requests, dynamicForwardedRequest{r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body})
		state.mu.Unlock()
		w.WriteHeader(204)
	}))
	m := NewDynamicManager(nil, nil, "", "", "", time.Second, time.Second)
	m.options.RuntimeAddr = backend.URL
	m.options.DNSAddr = backend.URL
	m.controlToken = "control-secret"
	m.brokerToken = "broker-secret"
	m.dispatcherToken = "dispatcher-secret"
	m.session = "unit-session"
	m.process = dynamicUnitProcess{}
	a := nanoflare.ActiveDeployment{App: nanoflare.App{ID: "app", OrgID: "org", Name: "worker"}, Deployment: nanoflare.Deployment{ID: "rev", AppID: "app", DNS: nanoflare.DNSSelection{Profile: "frozen-profile"}, KVNamespaces: []nanoflare.KVBinding{{Binding: "KV", ID: "declared-kv"}}}}
	snapshot, err := nanoflare.FreezeExecution(a, m.compileOptions)
	if err != nil {
		t.Fatal(err)
	}
	key := snapshot.ID()
	m.snapshots[key] = &dynamicSnapshot{frozen: snapshot, registeredSession: m.session, registered: true, validated: true}
	m.committed[dynamicIdentity(a)] = key
	m.active = []nanoflare.ActiveDeployment{a}
	t.Cleanup(func() {
		m.background.Wait()
		m.client.CloseIdleConnections()
		m.controlClient.CloseIdleConnections()
		backend.Close()
	})
	return m, state, a, key
}

func dynamicBrokerRequest(t *testing.T, m *DynamicManager, operation, key, token, binding string, body []byte, epochs ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/"+operation+"/"+key, bytes.NewReader(body))
	r.Header.Set("X-Nanoflare-Broker-Token", m.brokerToken)
	r.Header.Set("X-Nanoflare-Invocation", token)
	r.Header.Set("X-Nanoflare-Binding-Name", binding)
	if len(epochs) > 0 {
		r.Header.Set("X-Nanoflare-Invocation-Epoch", epochs[0])
	}
	w := httptest.NewRecorder()
	m.serveBroker(w, r)
	return w
}

func TestDynamicAdmissionResponseAndBackgroundLifetimes(t *testing.T) {
	for _, order := range []string{"never-dispatched", "complete-before-response", "background-after-response"} {
		t.Run(order, func(t *testing.T) {
			m, state, a, key := newDynamicControlFixture(t)
			endpoint, err := m.Ensure(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			token := endpoint.Headers.Get("X-Nanoflare-Invocation")
			if order == "never-dispatched" {
				endpoint.Release()
				endpoint.Release()
				if w := dynamicBrokerRequest(t, m, "start", key, token, "", nil); w.Code != 403 {
					t.Fatalf("canceled admission started user code: %d %s", w.Code, w.Body.String())
				}
			} else {
				if w := dynamicBrokerRequest(t, m, "start", key, token, "", nil); w.Code != 204 {
					t.Fatalf("start: %d %s", w.Code, w.Body.String())
				}
				if order == "complete-before-response" {
					for range 2 {
						if w := dynamicBrokerRequest(t, m, "complete", key, token, "", nil); w.Code != 204 {
							t.Fatalf("complete: %d %s", w.Code, w.Body.String())
						}
					}
				} else {
					endpoint.Release()
				}
				state.mu.Lock()
				for _, releases := range state.pins {
					if releases != 0 {
						t.Fatal("released while one lifetime remains")
					}
				}
				state.mu.Unlock()
				if order == "complete-before-response" {
					endpoint.Release()
				} else {
					if w := dynamicBrokerRequest(t, m, "complete", key, token, "", nil); w.Code != 204 {
						t.Fatalf("background completion: %d %s", w.Code, w.Body.String())
					}
				}
				endpoint.Release()
			}
			m.mu.Lock()
			remaining := len(m.invocations)
			m.mu.Unlock()
			if remaining != 0 {
				t.Fatal("finished invocation retained")
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if len(state.pins) != 1 {
				t.Fatalf("unexpected admissions: %#v", state.pins)
			}
			for _, releases := range state.pins {
				if releases != 1 {
					t.Fatalf("pin released %d times", releases)
				}
			}
		})
	}
}

func TestDynamicBrokerScopesResourcesToRevisionGrant(t *testing.T) {
	m, state, a, key := newDynamicControlFixture(t)
	endpoint, err := m.Ensure(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	token := endpoint.Headers.Get("X-Nanoflare-Invocation")
	defer endpoint.Release()
	if w := dynamicBrokerRequest(t, m, "start", key, token, "", nil); w.Code != 204 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	other := a
	other.Deployment.ID = "other"
	snapshot, _ := nanoflare.FreezeExecution(other, m.compileOptions)
	m.snapshots[snapshot.ID()] = &dynamicSnapshot{frozen: snapshot}
	for _, test := range []struct{ key, token, binding string }{
		{snapshot.ID(), token, "KV"}, {key, token, "UNDECLARED"},
	} {
		if w := dynamicBrokerRequest(t, m, "resource", test.key, test.token, test.binding, nil); w.Code != 403 {
			t.Fatalf("undeclared authority accepted: %d %s", w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/resource/"+key+"/hello?urlencoded=true", nil)
	r.Header.Set("X-Nanoflare-Broker-Token", m.brokerToken)
	r.Header.Set("X-Nanoflare-Invocation", token)
	r.Header.Set("X-Nanoflare-Binding-Name", "KV")
	r.Header.Set("X-Nanoflare-KV-Namespace-ID", "forged-namespace")
	r.Header.Set("X-Nanoflare-Runner-Token", "forged-control")
	r.Header.Set("X-Nanoflare-Database-ID", "forged-db")
	r.Header.Set("Authorization", "Bearer forged-token")
	w := httptest.NewRecorder()
	m.serveBroker(w, r)
	if w.Code != 204 {
		t.Fatalf("declared resource: %d %s", w.Code, w.Body.String())
	}
	if w := dynamicBrokerRequest(t, m, "complete", key, token, "", nil); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	endpoint.Release()
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.requests) != 1 {
		t.Fatalf("denied requests reached backend: %d", len(state.requests))
	}
	forwarded := state.requests[0]
	if forwarded.headers.Get("Authorization") != "Bearer "+key || forwarded.headers.Get("X-Nanoflare-KV-Namespace-ID") != "declared-kv" {
		t.Fatal("frozen authority not reconstructed", forwarded.headers)
	}
	for _, name := range []string{"X-Nanoflare-Runner-Token", "X-Nanoflare-Broker-Token", "X-Nanoflare-Database-ID", "X-Nanoflare-Invocation"} {
		if forwarded.headers.Get(name) != "" {
			t.Fatalf("caller control header forwarded: %s", name)
		}
	}
}

func TestDynamicAdmittedBackgroundServiceSurvivesRootResponse(t *testing.T) {
	m, state, caller, _ := newDynamicControlFixture(t)
	caller.Deployment.Services = []nanoflare.ServiceBinding{{Binding: "TARGET", Service: "target"}}
	snapshot, _ := nanoflare.FreezeExecution(caller, m.compileOptions)
	key := snapshot.ID()
	m.snapshots[key] = &dynamicSnapshot{frozen: snapshot}
	m.committed[dynamicIdentity(caller)] = key
	target := nanoflare.ActiveDeployment{App: nanoflare.App{ID: "target-app", OrgID: "org", Name: "target"}, Deployment: nanoflare.Deployment{ID: "target-rev", AppID: "target-app"}, TrafficPercent: 100}
	targetSnapshot, _ := nanoflare.FreezeExecution(target, m.compileOptions)
	m.snapshots[targetSnapshot.ID()] = &dynamicSnapshot{frozen: targetSnapshot}
	m.committed[dynamicIdentity(target)] = targetSnapshot.ID()
	// A same-name Worker in another organization must never enter selection.
	foreign := target
	foreign.App.ID = "foreign"
	foreign.App.OrgID = "other-org"
	foreign.Deployment.AppID = "foreign"
	foreignSnapshot, _ := nanoflare.FreezeExecution(foreign, m.compileOptions)
	m.snapshots[foreignSnapshot.ID()] = &dynamicSnapshot{frozen: foreignSnapshot}
	m.committed[dynamicIdentity(foreign)] = foreignSnapshot.ID()
	m.active = []nanoflare.ActiveDeployment{caller, target, foreign}
	endpoint, err := m.Ensure(context.Background(), caller)
	if err != nil {
		t.Fatal(err)
	}
	token := endpoint.Headers.Get("X-Nanoflare-Invocation")
	if w := dynamicBrokerRequest(t, m, "start", key, token, "", nil); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := dynamicBrokerRequest(t, m, "service", key+"/TARGET", token, "", nil)
	if w.Code != 200 {
		t.Fatalf("service admission: %d %s", w.Code, w.Body.String())
	}
	var child map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &child); err != nil {
		t.Fatal(err)
	}
	if child["key"] != targetSnapshot.ID() {
		t.Fatal("service escaped organization", child)
	}
	// Loading the child may cross response completion. Its parent explicitly
	// retained this call in waitUntil, so admission must survive that boundary.
	endpoint.Release()
	if w := dynamicBrokerRequest(t, m, "start", child["key"], child["token"], "", nil); w.Code != 204 {
		t.Fatalf("admitted background child canceled by response: %d %s", w.Code, w.Body.String())
	}
	if w := dynamicBrokerRequest(t, m, "complete", key, token, "", nil); w.Code != 204 {
		t.Fatalf("root complete: %d %s", w.Code, w.Body.String())
	}
	state.mu.Lock()
	for _, releases := range state.pins {
		if releases != 0 {
			t.Fatal("background child did not retain root lifetime")
		}
	}
	state.mu.Unlock()
	if w := dynamicBrokerRequest(t, m, "complete", child["key"], child["token"], "", nil); w.Code != 204 {
		t.Fatalf("child complete: %d %s", w.Code, w.Body.String())
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.pins) != 2 {
		t.Fatalf("expected root and service admission: %#v", state.pins)
	}
	for _, releases := range state.pins {
		if releases != 1 {
			t.Fatalf("admission release count: %d", releases)
		}
	}
}

func TestDynamicCompletedServiceCapabilityRetainsParentLifetime(t *testing.T) {
	m, _, active, key := newDynamicControlFixture(t)
	endpoint, err := m.Ensure(context.Background(), active)
	if err != nil {
		t.Fatal(err)
	}
	rootToken := endpoint.Headers.Get("X-Nanoflare-Invocation")
	if w := dynamicBrokerRequest(t, m, "start", key, rootToken, "", nil); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	m.mu.Lock()
	child, err := m.admitLocked(context.Background(), key, m.invocations[rootToken].root)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if w := dynamicBrokerRequest(t, m, "start", key, child.token, "", nil); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := dynamicBrokerRequest(t, m, "complete", key, child.token, "", nil); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := dynamicBrokerRequest(t, m, "resource", key, child.token, "KV", nil); w.Code != 204 {
		t.Fatalf("returned capability revoked before parent completion: %d %s", w.Code, w.Body.String())
	}
	if w := dynamicBrokerRequest(t, m, "complete", key, rootToken, "", nil); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	endpoint.Release()
	if w := dynamicBrokerRequest(t, m, "resource", key, "", "KV", nil); w.Code != 204 {
		t.Fatalf("published resource grant incorrectly expired with parent: %d %s", w.Code, w.Body.String())
	}
	if w := dynamicBrokerRequest(t, m, "start", key, child.token, "", nil, "2"); w.Code != 403 {
		t.Fatal("completed invocation was revived")
	}
}

func TestDynamicSnapshotRetirementPrecedesConcurrentRepublication(t *testing.T) {
	m, _, active, oldKey := newDynamicControlFixture(t)
	m.committed = map[string]string{}
	retiring := make(chan struct{})
	allowRetire := make(chan struct{})
	registered := make(chan struct{}, 1)
	var authorityMu sync.Mutex
	accepting := map[string]bool{oldKey: true}
	m.controlClient.Transport = dynamicUnitTransport(func(r *http.Request) (*http.Response, error) {
		body := `{}`
		switch {
		case strings.HasSuffix(r.URL.Path, "/retire"):
			close(retiring)
			<-allowRetire
			authorityMu.Lock()
			accepting[oldKey] = false
			authorityMu.Unlock()
		case strings.HasSuffix(r.URL.Path, "/register"):
			body = dynamicGrantRegistrationResponse(t, r)
			var result map[string]string
			json.Unmarshal([]byte(body), &result)
			authorityMu.Lock()
			accepting[result["execution_id"]] = true
			authorityMu.Unlock()
			registered <- struct{}{}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	m.client.Transport = dynamicUnitTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	collected := make(chan struct{})
	go func() { m.collectSnapshots(); close(collected) }()
	<-retiring
	built := make(chan error, 1)
	var newKey string
	go func() {
		keys, err := m.build([]nanoflare.ActiveDeployment{active}, false, "")
		newKey = keys[dynamicIdentity(active)]
		built <- err
	}()
	// Fresh authority can register while an old grant retires. The delayed
	// retirement must affect only the old key, without blocking new admissions.
	select {
	case <-registered:
	case <-time.After(100 * time.Millisecond):
	}
	close(allowRetire)
	if err := <-built; err != nil {
		t.Fatal(err)
	}
	<-collected
	authorityMu.Lock()
	defer authorityMu.Unlock()
	if newKey == oldKey || !accepting[newKey] || accepting[oldKey] {
		t.Fatal("delayed old retirement revoked newly published execution")
	}
}

func TestDynamicFailedRetirementRemainsRetryable(t *testing.T) {
	m, _, _, key := newDynamicControlFixture(t)
	m.committed = map[string]string{}
	attempts := 0
	m.controlClient.Transport = dynamicUnitTransport(func(r *http.Request) (*http.Response, error) {
		attempts++
		status := 200
		if attempts == 1 {
			status = 503
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	m.collectSnapshots()
	if m.snapshots[key] == nil {
		t.Fatal("failed retirement discarded reconciliation state")
	}
	m.collectSnapshots()
	if m.snapshots[key] != nil || attempts != 2 {
		t.Fatal("retirement was not retried")
	}
}

func TestDynamicPrepareRetainsCandidatesThroughRoutingCopy(t *testing.T) {
	m, _, active, _ := newDynamicControlFixture(t)
	// Make the detached routing copy long enough to exercise cleanup after build.
	active.Deployment.Assets = []nanoflare.AssetFile{{Path: "large.bin", Data: make([]byte, 8<<20)}}
	snapshot, err := nanoflare.FreezeExecution(active, m.compileOptions)
	if err != nil {
		t.Fatal(err)
	}
	registration := "prepare-test"
	key := nanoflare.ResourceGrantID(m.session, registration)
	m.snapshots = map[string]*dynamicSnapshot{key: {frozen: snapshot, registeredSession: m.session, registration: registration, validated: true}}
	m.committed = map[string]string{}
	registered := make(chan struct{})
	m.controlClient.Transport = dynamicUnitTransport(func(r *http.Request) (*http.Response, error) {
		body := `{}`
		if strings.HasSuffix(r.URL.Path, "/register") {
			body = dynamicGrantRegistrationResponse(t, r)
			close(registered)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	stop := make(chan struct{})
	done := make(chan struct{})
	defer func() { close(stop); <-done }()
	go func() {
		defer close(done)
		select {
		case <-registered:
		case <-stop:
			return
		}
		for {
			m.mu.Lock()
			staging, pending := m.staging, m.pending != nil
			m.mu.Unlock()
			if pending {
				m.collectSnapshots()
				return
			}
			if staging == 0 {
				m.collectSnapshots()
				return
			}
			select {
			case <-stop:
				return
			default:
				goruntime.Gosched()
			}
		}
	}()
	if _, _, err := m.Prepare([]nanoflare.ActiveDeployment{active}); err != nil {
		t.Fatal(err)
	}
	<-done
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.snapshots[key] == nil || m.pending == nil || m.pending.keys[dynamicIdentity(active)] != key {
		t.Fatal("prepared publication lost its candidate during cleanup")
	}
}

func TestDynamicResourcePathsCannotReachControlNamespace(t *testing.T) {
	m, state, active, key := newDynamicControlFixture(t)
	endpoint, err := m.Ensure(context.Background(), active)
	if err != nil {
		t.Fatal(err)
	}
	token := endpoint.Headers.Get("X-Nanoflare-Invocation")
	if w := dynamicBrokerRequest(t, m, "start", key, token, "", nil); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"internal/runtime/durations", "internal/runtime/executions/close-session"} {
		r := httptest.NewRequest(http.MethodPost, "/resource/"+key+"/"+path, nil)
		r.Header.Set("X-Nanoflare-Broker-Token", m.brokerToken)
		r.Header.Set("X-Nanoflare-Invocation", token)
		r.Header.Set("X-Nanoflare-Binding-Name", "KV")
		w := httptest.NewRecorder()
		m.serveBroker(w, r)
		if w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := dynamicBrokerRequest(t, m, "complete", key, token, "", nil); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	endpoint.Release()
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.requests) != 2 {
		t.Fatalf("resource request entered control namespace: %#v", state.requests)
	}
	for _, forwarded := range state.requests {
		if !strings.HasPrefix(forwarded.path, "/internal/runtime/resources/") {
			t.Fatal("resource path escaped isolated namespace", forwarded.path)
		}
	}
}

func TestDynamicReturnedCapabilityEpochRetainsNewBackgroundWork(t *testing.T) {
	m, state, active, key := newDynamicControlFixture(t)
	endpoint, err := m.Ensure(context.Background(), active)
	if err != nil {
		t.Fatal(err)
	}
	token := endpoint.Headers.Get("X-Nanoflare-Invocation")
	for _, step := range []struct{ operation, epoch string }{
		{"start", "1"}, {"complete", "1"}, {"start", "2"}, {"complete", "1"}, {"start", "2"},
	} {
		if w := dynamicBrokerRequest(t, m, step.operation, key, token, "", nil, step.epoch); w.Code != 204 {
			t.Fatalf("%s epoch%s: %d %s", step.operation, step.epoch, w.Code, w.Body.String())
		}
	}
	endpoint.Release()
	m.mu.Lock()
	remaining := len(m.invocations)
	m.mu.Unlock()
	if remaining != 1 {
		t.Fatal("new RPC epoch background work lost invocation")
	}
	state.mu.Lock()
	for _, releases := range state.pins {
		if releases != 0 {
			t.Fatal("stale completion released current epoch")
		}
	}
	state.mu.Unlock()
	if w := dynamicBrokerRequest(t, m, "complete", key, token, "", nil, "2"); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, releases := range state.pins {
		if releases != 1 {
			t.Fatal("final epoch did not release lifetime")
		}
	}
}

func TestDynamicFailedReleaseRetriesWithoutLosingPin(t *testing.T) {
	m, state, active, key := newDynamicControlFixture(t)
	base := m.controlClient.Transport
	attempts := 0
	m.controlClient.Transport = dynamicUnitTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/release") {
			attempts++
			if attempts == 1 {
				return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("temporary"))}, nil
			}
		}
		return base.RoundTrip(r)
	})
	endpoint, err := m.Ensure(context.Background(), active)
	if err != nil {
		t.Fatal(err)
	}
	token := endpoint.Headers.Get("X-Nanoflare-Invocation")
	if w := dynamicBrokerRequest(t, m, "start", key, token, "", nil); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := dynamicBrokerRequest(t, m, "complete", key, token, "", nil); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	endpoint.Release()
	if len(m.pendingReleases) != 1 {
		t.Fatal("failed release lost reconciliation state")
	}
	m.flushReleases()
	m.flushReleases()
	if len(m.pendingReleases) != 0 || attempts != 2 {
		t.Fatal("release queue did not reconcile idempotently")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, releases := range state.pins {
		if releases != 1 {
			t.Fatal("central pin not released exactly once")
		}
	}
}

func TestDynamicLostAdmissionResponseCancelsKnownPin(t *testing.T) {
	m, state, active, _ := newDynamicControlFixture(t)
	base := m.controlClient.Transport
	releases := 0
	m.controlClient.Transport = dynamicUnitTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/admit") {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			state.mu.Lock()
			state.pins[body["session_id"]+":"+body["admission_id"]] = 0
			state.mu.Unlock()
			return nil, fmt.Errorf("admission accepted but response lost")
		}
		if strings.HasSuffix(r.URL.Path, "/release") {
			releases++
			if releases == 1 {
				return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("temporary"))}, nil
			}
		}
		return base.RoundTrip(r)
	})
	if _, err := m.Ensure(context.Background(), active); err == nil {
		t.Fatal("lost admission response unexpectedly succeeded")
	}
	m.background.Wait()
	if len(m.invocations) != 0 || m.pendingReleases["unit-session:1"] != "unit-session" {
		t.Fatal("ambiguous admission lost its deterministic cancellation handle")
	}
	m.flushReleases()
	if len(m.pendingReleases) != 0 {
		t.Fatal("ambiguous admission cancellation did not reconcile")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pins["unit-session:1"] != 1 {
		t.Fatal("unknown accepted admission leaked")
	}
}

func TestDynamicSyntheticBindingsFreezeDNSAndTelemetryAttribution(t *testing.T) {
	m, state, _, key := newDynamicControlFixture(t)
	r := httptest.NewRequest(http.MethodPost, "/resource/"+key+"/caller-path?caller-query=true", strings.NewReader(`{"name":"example.com"}`))
	r.Header.Set("X-Nanoflare-Broker-Token", m.brokerToken)
	r.Header.Set("X-Nanoflare-Binding-Name", "__NANOFLARE_DNS")
	r.Header.Set("X-Nanoflare-DNS-Profile", "forged-profile")
	w := httptest.NewRecorder()
	m.serveBroker(w, r)
	if w.Code != 204 {
		t.Fatalf("DNS: %d %s", w.Code, w.Body.String())
	}
	events := []DurationTraceEvent{{ScriptName: "forged-app"}}
	body, _ := json.Marshal(events)
	w = dynamicBrokerRequest(t, m, "resource", key, "", "__NANOFLARE_DURATION_COLLECTOR", body)
	if w.Code != 204 {
		t.Fatalf("telemetry: %d %s", w.Code, w.Body.String())
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.requests) != 2 {
		t.Fatalf("forwarded requests: %d", len(state.requests))
	}
	dns := state.requests[0]
	if dns.path != "/lookup" || dns.query != "" || dns.headers.Get("X-Nanoflare-DNS-Profile") != "frozen-profile" {
		t.Fatalf("DNS authority not frozen: %#v", dns)
	}
	telemetry := state.requests[1]
	if telemetry.path != "/internal/runtime/durations" {
		t.Fatal("telemetry endpoint not fixed", telemetry.path)
	}
	if err := json.Unmarshal(telemetry.body, &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ScriptName != "app" {
		t.Fatalf("telemetry attribution not frozen: %#v", events)
	}
}

func dynamicGrantRegistrationResponse(t *testing.T, r *http.Request) string {
	t.Helper()
	var input struct {
		Active         nanoflare.ActiveDeployment `json:"active"`
		Secrets        map[string]string          `json:"secret_values"`
		ObjectKey      string                     `json:"object_key"`
		CompileOptions string                     `json:"compile_options"`
		Session        string                     `json:"session_id"`
		Registration   string                     `json:"registration_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		t.Error(err)
		return "{}"
	}
	input.Active.App.SecretValues = input.Secrets
	input.Active.Deployment.ObjectKey = input.ObjectKey
	snapshot, err := nanoflare.FreezeExecution(input.Active, input.CompileOptions)
	if err != nil {
		t.Error(err)
		return "{}"
	}
	return fmt.Sprintf(`{"execution_id":%q,"snapshot_id":%q}`, nanoflare.ResourceGrantID(input.Session, input.Registration), snapshot.ID())
}

func TestDynamicRetiredSnapshotGetsFreshGrantWithoutResettingPublishedSnapshot(t *testing.T) {
	m, _, active, oldKey := newDynamicControlFixture(t)
	m.controlClient.Transport = dynamicUnitTransport(func(r *http.Request) (*http.Response, error) {
		body := dynamicGrantRegistrationResponse(t, r)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	m.client.Transport = dynamicUnitTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	keys, err := m.build([]nanoflare.ActiveDeployment{active}, false, "")
	if err != nil || keys[dynamicIdentity(active)] != oldKey {
		t.Fatal("unchanged publication discarded warm revision", keys, err)
	}
	// Keep the old snapshot resident, as when a retired WebSocket is draining.
	m.snapshots[oldKey].retired = true
	keys, err = m.build([]nanoflare.ActiveDeployment{active}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	newKey := keys[dynamicIdentity(active)]
	if newKey == oldKey || m.snapshots[newKey].frozen.ID() != m.snapshots[oldKey].frozen.ID() {
		t.Fatal("identical content revived retired grant or changed snapshot identity")
	}
	if !m.snapshots[oldKey].retired || m.snapshots[newKey].retired {
		t.Fatal("republication mutated the old grant's retirement state")
	}
}

type dynamicRecoveryTestProcess struct {
	stops   int
	stopErr error
}

func (*dynamicRecoveryTestProcess) Done() <-chan error { return make(chan error) }
func (p *dynamicRecoveryTestProcess) Stop(context.Context) error {
	p.stops++
	return p.stopErr
}

func TestDynamicRecoverySkipsReplacementAfterWaitingForPublication(t *testing.T) {
	for _, change := range []string{"process", "session", "process-and-session"} {
		t.Run(change, func(t *testing.T) {
			m, _, active, key := newDynamicControlFixture(t)
			old := &dynamicRecoveryTestProcess{}
			m.process = old
			observed := dynamicRecoveryState{process: old, session: m.session}
			replacement := &dynamicRecoveryTestProcess{}
			m.publication.Lock()
			started, done := make(chan struct{}), make(chan error, 1)
			go func() {
				close(started)
				done <- m.recoverSession(&observed)
			}()
			<-started
			// A publisher or another recovery wins while this attempt waits.
			m.mu.Lock()
			if change != "session" {
				m.process = replacement
			}
			if change != "process" {
				m.session = "replacement-session"
			}
			m.mu.Unlock()
			m.publication.Unlock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if old.stops != 0 || replacement.stops != 0 {
				t.Fatal("stale recovery stopped a process")
			}
			if m.committed[dynamicIdentity(active)] != key || m.snapshots[key] == nil {
				t.Fatal("stale recovery changed committed snapshots")
			}
		})
	}
}

func TestDynamicRecoveryFailureRetriesOnlyItsOwnPartialState(t *testing.T) {
	m, _, _, _ := newDynamicControlFixture(t)
	process := &dynamicRecoveryTestProcess{stopErr: errors.New("stop failed")}
	m.process = process
	observed := dynamicRecoveryState{process: process, session: m.session}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := m.recoverSession(&observed); err == nil {
			t.Fatal("failed stop unexpectedly recovered")
		}
		if process.stops != attempt || observed.process != process || observed.session != "" {
			t.Fatal("retry lost ownership of its partial recovery state")
		}
	}
	replacement := &dynamicRecoveryTestProcess{}
	m.process, m.session = replacement, "replacement-session"
	if err := m.recoverSession(&observed); err != nil {
		t.Fatal(err)
	}
	if replacement.stops != 0 || m.process != replacement || m.session != "replacement-session" {
		t.Fatal("retry disturbed a replacement installed after its failure")
	}
}

func TestDynamicCrashWatcherLeavesHealthyReplacementRunning(t *testing.T) {
	m, _, _, _ := newDynamicControlFixture(t)
	old := &fakeProcess{done: make(chan error, 1), events: &eventLog{}}
	m.process = old
	m.background.Add(1)
	done := make(chan struct{})
	go func() { defer close(done); m.watchDynamic(old, "") }()
	t.Cleanup(func() { close(m.stop); <-done })
	old.crash(errors.New("original dispatcher crashed"))
	deadline := time.Now().Add(time.Second)
	for {
		m.mu.Lock()
		observed := m.process == nil
		m.mu.Unlock()
		if observed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watcher did not observe the original crash")
		}
		time.Sleep(time.Millisecond)
	}
	replacement := &dynamicRecoveryTestProcess{stopErr: errors.New("healthy replacement must not stop")}
	m.publication.Lock()
	m.mu.Lock()
	m.process, m.session = replacement, "replacement-session"
	m.mu.Unlock()
	m.publication.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stale watcher kept trying to recover after replacement")
	}
	if replacement.stops != 0 || m.process != replacement || m.session != "replacement-session" {
		t.Fatal("stale watcher disturbed the healthy replacement")
	}
}
