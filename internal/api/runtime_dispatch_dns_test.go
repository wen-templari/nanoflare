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
	if w := request(token, lease); w.Code != 429 {
		t.Fatalf("budget: %d %s", w.Code, w.Body)
	}
	if calls != 100 || queries.Load() == 0 {
		t.Fatalf("adapter calls=%d DNS queries=%d", calls, queries.Load())
	}
	if err := service.FinishDispatch(context.Background(), token, lease, true); err != nil {
		t.Fatal(err)
	}
	if w := request(token, lease); w.Code == 200 {
		t.Fatal("expired lease reached DNS")
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
	go func() { handler.ServeHTTP(httptest.NewRecorder(), request); close(completed) }()
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
							{Path: "dist/lib/probe.js", Content: `import dns from 'node:dns'; import {lookup} from 'node:dns/promises'; import bare from 'dns'; import barePromises from 'dns/promises';
export async function probe(){
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
