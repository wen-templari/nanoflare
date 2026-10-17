package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	m.snapshots[key] = &dynamicSnapshot{frozen: snapshot, registeredSession: m.session, validated: true}
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

func TestDynamicBrokerScopesResourcesToInvocationRevision(t *testing.T) {
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
		{snapshot.ID(), token, "KV"}, {key, "forged", "KV"}, {key, token, "UNDECLARED"},
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
	if forwarded.headers.Get("Authorization") != "Bearer "+token || forwarded.headers.Get("X-Nanoflare-KV-Namespace-ID") != "declared-kv" {
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
	if w := dynamicBrokerRequest(t, m, "resource", key, child.token, "KV", nil); w.Code != 403 {
		t.Fatalf("returned capability survived parent completion: %d %s", w.Code, w.Body.String())
	}
}

func TestDynamicSnapshotRetirementPrecedesConcurrentRepublication(t *testing.T) {
	m, _, active, key := newDynamicControlFixture(t)
	m.committed = map[string]string{}
	retiring := make(chan struct{})
	allowRetire := make(chan struct{})
	registered := make(chan struct{}, 1)
	var authorityMu sync.Mutex
	accepting := true
	m.controlClient.Transport = dynamicUnitTransport(func(r *http.Request) (*http.Response, error) {
		body := `{}`
		switch {
		case strings.HasSuffix(r.URL.Path, "/retire"):
			close(retiring)
			<-allowRetire
			authorityMu.Lock()
			accepting = false
			authorityMu.Unlock()
		case strings.HasSuffix(r.URL.Path, "/register"):
			authorityMu.Lock()
			accepting = true
			authorityMu.Unlock()
			registered <- struct{}{}
			body = fmt.Sprintf(`{"execution_id":%q}`, key)
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
	go func() { _, err := m.build([]nanoflare.ActiveDeployment{active}); built <- err }()
	// A previously issued retirement is deliberately delayed while a new
	// publication of the same execution starts. Registration must run after it.
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
	if !accepting {
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
