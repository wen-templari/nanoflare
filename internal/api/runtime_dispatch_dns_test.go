package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/dnsresolver"
	"github.com/clas/nanoflare/internal/nanoflare"
	"golang.org/x/net/dns/dnsmessage"
)

// This name exists only on the fixture's private resolver, never public DNS.
func privateDNSFixture(t *testing.T) (http.Handler, *atomic.Int32) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	var queries atomic.Int32
	go func() {
		buffer := make([]byte, 1500)
		for {
			n, client, err := conn.ReadFrom(buffer)
			if err != nil {
				return
			}
			var parser dnsmessage.Parser
			header, err := parser.Start(buffer[:n])
			if err != nil {
				continue
			}
			q, err := parser.Question()
			if err != nil {
				continue
			}
			queries.Add(1)
			b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: header.ID, Response: true, RecursionAvailable: true})
			b.StartQuestions()
			b.Question(q)
			b.StartAnswers()
			if q.Name.String() == "database.corporate.invalid." && q.Type == dnsmessage.TypeA {
				b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 1}, dnsmessage.AResource{A: [4]byte{10, 20, 30, 40}})
			}
			response, err := b.Finish()
			if err == nil {
				conn.WriteTo(response, client)
			}
		}
	}()
	resolver, err := dnsresolver.New(dnsresolver.Config{DefaultProfile: "corporate", Profiles: map[string]dnsresolver.Profile{"corporate": {Servers: []string{conn.LocalAddr().String()}, Timeout: "1s"}}}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return resolver, &queries
}

func dnsDispatchLease(t *testing.T) (*nanoflare.Service, string, string) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	repo := nanoflare.NewStore()
	must(repo.CreateOrganization(nanoflare.Organization{ID: "dns-org", Name: "DNS", UsageLevel: nanoflare.UsageLevelPaid}))
	service := nanoflare.NewService(repo, discardWriter{})
	service.SetPlatformDispatchEnabled(true)
	ns, err := service.CreateDispatchNamespace("dns-org", nanoflare.CreateDispatchNamespaceInput{Name: "dns"})
	must(err)
	customer, err := service.CreateNamespaceScript("dns-org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
	must(err)
	input := nanoflare.DeployInput{Format: "modules", CompatibilityDate: "2026-09-11", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}}
	_, err = service.Deploy(customer.ID, input)
	must(err)
	platform, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "dns-org", Name: "platform", Hostname: "dns.example.com"})
	must(err)
	input.DispatchNamespaces = []nanoflare.DispatchBinding{{Binding: "NS", NamespaceID: ns.ID}}
	deployment, err := service.Deploy(platform.ID, input)
	must(err)
	token := nanoflare.RuntimeCapability(nanoflare.ActiveDeployment{App: platform, Deployment: deployment}, nanoflare.RuntimeDispatch, ns.ID)
	artifact, err := service.AcquireDispatch(context.Background(), token, ns.ID, "alpha")
	must(err)
	t.Cleanup(func() { service.FinishDispatch(context.Background(), token, artifact.Lease, true) })
	return service, token, artifact.Lease
}

func TestDispatchDNSDefaultProfileAndAdmission(t *testing.T) {
	resolver, queries := privateDNSFixture(t)
	service, token, lease := dnsDispatchLease(t)
	var calls int
	handler := NewRuntimeDispatchServer(service, nil).SetDNSHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/lookup" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Nanoflare-DNS-Profile") != "" {
			t.Errorf("caller data reached resolver: %v %v", r.URL, r.Header)
		}
		resolver.ServeHTTP(w, r)
	}))
	request := func(auth, invocation string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://runtime/internal/runtime/dispatch/dns?profile=system", strings.NewReader(`{"hostname":"database.corporate.invalid","family":4}`))
		r.Header.Set("Authorization", "Bearer "+auth)
		r.Header.Set("X-Nanoflare-Invocation", invocation)
		r.Header.Set("X-Nanoflare-DNS-Profile", "system")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request("wrong", lease); w.Code == 200 {
		t.Fatal("accepted wrong transport")
	}
	if w := request(token, "nfi1.invalid"); w.Code == 200 {
		t.Fatal("accepted missing lease")
	}
	if calls != 0 {
		t.Fatal("unauthorized DNS reached adapter")
	}
	for i := 0; i < 100; i++ {
		if w := request(token, lease); w.Code != 200 || !strings.Contains(w.Body.String(), "10.20.30.40") {
			t.Fatalf("lookup %d: %d %s", i, w.Code, w.Body)
		}
	}
	if w := request(token, lease); w.Code != 429 || !strings.Contains(w.Body.String(), "ERR_NANOFLARE_EFFECT_LIMIT") {
		t.Fatalf("budget: %d %s", w.Code, w.Body)
	}
	if calls != 100 || queries.Load() == 0 {
		t.Fatalf("adapter calls=%d DNS queries=%d", calls, queries.Load())
	}
	if err := service.FinishDispatch(context.Background(), token, lease, true); err != nil {
		t.Fatal(err)
	}
	if w := request(token, lease); w.Code != 410 || !strings.Contains(w.Body.String(), "ERR_NANOFLARE_INVOCATION_EXPIRED") {
		t.Fatalf("expired lease: %d %s", w.Code, w.Body)
	}
	if calls != 100 {
		t.Fatal("expired DNS reached adapter")
	}
}

func TestDispatchDNSCancellation(t *testing.T) {
	service, token, lease := dnsDispatchLease(t)
	started, done := make(chan struct{}), make(chan struct{})
	handler := NewRuntimeDispatchServer(service, nil).SetDNSHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(done) }))
	request := httptest.NewRequest("POST", "/internal/runtime/dispatch/dns", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Nanoflare-Invocation", lease)
	completed := make(chan struct{})
	response := httptest.NewRecorder()
	go func() { handler.ServeHTTP(response, request); close(completed) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("lookup did not start")
	}
	if err := service.FinishDispatch(context.Background(), token, lease, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("DNS did not cancel with lease")
	}
	<-completed
	if response.Code != 410 || !strings.Contains(response.Body.String(), "ERR_NANOFLARE_INVOCATION_EXPIRED") {
		t.Fatalf("cancellation: %d %s", response.Code, response.Body)
	}
}

func TestWorkerdDynamicNamespacePrivateDNS(t *testing.T) {
	if os.Getenv("NANOFLARE_TEST_WORKERD") == "" {
		t.Skip("set NANOFLARE_TEST_WORKERD to pinned workerd")
	}
	for _, dynamic := range []bool{false, true} {
		for _, remote := range []bool{false, true} {
			t.Run(fmt.Sprintf("dynamic=%t/remote=%t", dynamic, remote), func(t *testing.T) {
				resolver, queries := privateDNSFixture(t)
				privateDynamicDispatchWithFixture(t, remote, dynamic, func(server *Server, service *nanoflare.Service, org nanoflare.Organization, _ string) {
					must := func(err error) {
						t.Helper()
						if err != nil {
							t.Fatal(err)
						}
					}
					ns, err := service.CreateDispatchNamespace(org.ID, nanoflare.CreateDispatchNamespaceInput{Name: "dns"})
					must(err)
					for _, name := range []string{"object", "class"} {
						app, err := service.CreateNamespaceScript(org.ID, ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: name, Execution: "dynamic"})
						must(err)
						source := `import {probe} from './lib/probe.js'; export default {fetch(){return probe()}};`
						if name == "class" {
							source = `import {WorkerEntrypoint} from 'cloudflare:workers'; import {probe} from './lib/probe.js'; export default class extends WorkerEntrypoint {fetch(){return probe()}}`
						}
						_, err = service.Deploy(app.ID, nanoflare.DeployInput{Format: "modules", Entrypoint: "dist/worker.js", CompatibilityDate: "2026-09-11", CompatibilityFlags: []string{"nodejs_compat"}, Files: []nanoflare.WorkerFile{
							{Path: "dist/worker.js", Content: source},
							{Path: "dist/lib/probe.js", Content: `const initializedDNS = await import('node:dns');
import dns from 'node:dns'; import {lookup} from 'node:dns/promises'; import bare from 'dns'; import barePromises from 'dns/promises';
export async function probe(){
 if ('a b'.charCodeAt(1)!==160 || !/ /.test('a b')) throw new Error('namespace Unicode changed');
 const host='database.corporate.invalid',options={all:true,family:4};
 await scheduler.wait(Math.floor(Math.random()*15));
 const callback=await new Promise((resolve,reject)=>dns.lookup(host,options,(err,value)=>err?reject(err):resolve(value)));
 return Response.json([callback,await dns.promises.lookup(host,options),await lookup(host,options),await bare.promises.lookup(host,options),await barePromises.lookup(host,options)]);
}`},
						}})
						must(err)
					}
					platform, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "dns-platform", Hostname: "dns.example.com"})
					must(err)
					_, err = service.Deploy(platform.ID, nanoflare.DeployInput{Format: "modules", CompatibilityDate: "2026-09-11", DispatchNamespaces: []nanoflare.DispatchBinding{{Binding: "NS", NamespaceID: ns.ID}}, Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {fetch(r,e){return e.NS.get(new URL(r.url).pathname.split('/').pop()).fetch(r)}}`}}})
					must(err)
					public := httptest.NewServer(server)
					defer public.Close()
					// Reuse each revision over concurrent and subsequent invocations.
					for round := 0; round < 2; round++ {
						if round > 0 {
							// Previous leases have a two-second background grace. Reuse code
							// only after those leases expire to detect a cached broker.
							time.Sleep(2100 * time.Millisecond)
						}
						var wg sync.WaitGroup
						for i := 0; i < 8; i++ {
							wg.Add(1)
							go func(i int) {
								defer wg.Done()
								name := []string{"object", "class"}[i%2]
								response, err := public.Client().Get(public.URL + "/internal/http/workers/" + platform.ID + "/" + name)
								if err != nil {
									t.Error(err)
									return
								}
								defer response.Body.Close()
								body, _ := io.ReadAll(response.Body)
								var result [][]struct {
									Address string `json:"address"`
									Family  int    `json:"family"`
								}
								if response.StatusCode != 200 || json.Unmarshal(body, &result) != nil || len(result) != 5 {
									t.Errorf("DNS dispatch: %d %s", response.StatusCode, body)
									return
								}
								for _, addresses := range result {
									if len(addresses) != 1 || addresses[0].Address != "10.20.30.40" || addresses[0].Family != 4 {
										t.Errorf("private DNS answer: %s", body)
									}
								}
							}(i)
						}
						wg.Wait()
					}
					if queries.Load() == 0 {
						t.Error("private DNS was never queried")
					}
				}, dispatchFixtureObservations{DNS: resolver})
			})
		}
	}
}

func TestDispatchDNSUnavailableDoesNotConsumeBudget(t *testing.T) {
	service, token, lease := dnsDispatchLease(t)
	handler := NewRuntimeDispatchServer(service, nil)
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/internal/runtime/dispatch/dns", strings.NewReader(`{"hostname":"example.com"}`))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Nanoflare-Invocation", lease)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for i := 0; i < 101; i++ {
		w := request()
		var body struct {
			Code string `json:"code"`
		}
		if w.Code != 503 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Code != "EAI_AGAIN" {
			t.Fatalf("unavailable: %d %s", w.Code, w.Body)
		}
	}
	handler.SetDNSHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for i := 0; i < 100; i++ {
		if w := request(); w.Code != 204 {
			t.Fatalf("lookup %d: %d %s", i, w.Code, w.Body)
		}
	}
	if w := request(); w.Code != 429 {
		t.Fatalf("budget: %d", w.Code)
	}
}

func TestDispatchDNSBrokerErrorsAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{nanoflare.ErrInvocationExpired, 410, "ERR_NANOFLARE_INVOCATION_EXPIRED"},
		{nanoflare.ErrSubrequestLimit, 429, "ERR_NANOFLARE_EFFECT_LIMIT"},
		{nanoflare.ErrRuntimeGrantDenied, 403, "ERR_NANOFLARE_ACCESS_DENIED"},
		{nanoflare.ErrInvalidCapability, 401, "ERR_NANOFLARE_ACCESS_DENIED"},
	} {
		w := httptest.NewRecorder()
		writeDNSBrokerError(w, tc.err)
		var body struct {
			Code, Detail string
			Status       int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != tc.status || body.Code != tc.code || body.Detail != tc.err.Error() || body.Status != tc.status {
			t.Fatalf("broker error: %d %s", w.Code, w.Body)
		}
	}
}

func TestDispatchArtifactRejectsCollisionAndFinishesLease(t *testing.T) {
	service, token, lease := dnsDispatchLease(t)
	artifact := nanoflare.DispatchArtifact{Lease: lease, Deployment: nanoflare.Deployment{Files: []nanoflare.WorkerFile{{Path: "nanoflare-internal:dns", Content: "export default {}"}}}}
	r := httptest.NewRequest("POST", "/internal/runtime/dispatch/acquire", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	NewRuntimeDispatchServer(service, nil).writeDispatchArtifact(w, r, artifact)
	if w.Code != 422 {
		t.Fatalf("collision: %d %s", w.Code, w.Body)
	}
	if _, err := service.DispatchEffect(context.Background(), token, lease, nanoflare.RuntimeDNS, "", 1); err != nanoflare.ErrInvocationExpired {
		t.Fatalf("lease not finished: %v", err)
	}
}

func TestDispatchDNSDeadlineOverridesResolverTimeout(t *testing.T) {
	service, token, lease := dnsDispatchLease(t)
	handler := NewRuntimeDispatchServer(service, nil).SetDNSHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		writeJSON(w, 502, map[string]string{"code": "ETIMEOUT", "error": "resolver deadline"})
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("POST", "/internal/runtime/dispatch/dns", strings.NewReader(`{}`)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Nanoflare-Invocation", lease)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 410 || !strings.Contains(w.Body.String(), "ERR_NANOFLARE_INVOCATION_EXPIRED") {
		t.Fatalf("deadline disguised as resolver timeout: %d %s", w.Code, w.Body)
	}
}

func TestDispatchDNSStatusMatchesRuntimeStatus(t *testing.T) {
	for _, err := range []error{nanoflare.ErrInvocationExpired, nanoflare.ErrRuntimeGrantDenied, nanoflare.ErrInvalidCapability, nanoflare.ErrSubrequestLimit, nanoflare.ErrArtifactNotFound, nanoflare.ErrUsageLimitExceeded} {
		runtime, dns := httptest.NewRecorder(), httptest.NewRecorder()
		writeRuntimeError(runtime, err)
		writeDNSBrokerError(dns, err)
		if runtime.Code != dns.Code {
			t.Fatalf("status drift for %v: %d vs %d", err, runtime.Code, dns.Code)
		}
	}
}

func TestNamespaceDeploymentRejectsDNSParseFailure(t *testing.T) {
	service, _, _ := dnsDispatchLease(t)
	service.SetNamespaceCodeValidator(func(deployment nanoflare.Deployment) error {
		_, err := config.NamespaceWorkerModules(deployment)
		return err
	})
	active, err := service.ActiveDeployments()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range active {
		if item.App.Platform == nil || item.App.Platform.NamespaceID == "" {
			continue
		}
		_, err := service.Deploy(item.App.ID, nanoflare.DeployInput{Format: "modules", CompatibilityDate: "2026-09-11", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `import dns from 'node:dns'; export default <div/>;`}}})
		if err == nil || !strings.Contains(err.Error(), "DNS import analysis") {
			t.Fatalf("deployment accepted parse failure: %v", err)
		}
		return
	}
	t.Fatal("namespace fixture missing")
}
