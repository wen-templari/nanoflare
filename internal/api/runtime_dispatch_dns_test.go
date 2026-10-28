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
			if q.Name.String() == "database.corporate.invalid." && q.Type == dnsmessage.TypeAAAA {
				b.AAAAResource(dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET, TTL: 1}, dnsmessage.AAAAResource{AAAA: [16]byte{0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}})
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
export async function probe(){ try {
 if ('a b'.charCodeAt(1)!==160 || !/ /.test('a b')) throw new Error('namespace Unicode changed');
 const host='database.corporate.invalid',options={all:true,family:4};
 await scheduler.wait(Math.floor(Math.random()*15));
 const callback=await new Promise((resolve,reject)=>dns.lookup(host,options,(err,value)=>err?reject(err):resolve(value)));
 const missing = await dns.promises.lookup('missing.corporate.invalid',options).then(()=>false,e=>e.code==='ENOTFOUND');
 const unsupported = await dns.promises.resolveTxt(host).then(()=>false,e=>e.code==='ENOTFOUND');
 if (!missing || !unsupported) throw new Error('DNS failure contract changed: '+JSON.stringify({missing,unsupported}));
 return Response.json([callback,await dns.promises.lookup(host,options),await lookup(host,options),await bare.promises.lookup(host,options),await barePromises.lookup(host,options),
 (await dns.promises.resolve4(host)).map(address=>({address,family:4})),(await dns.promises.resolve6(host)).map(address=>({address,family:6})),
 await dns.promises.lookup(host,{all:true}),await dns.promises.lookup(host,{all:true,family:6})]);
} catch(error) {return Response.json({message:error.message,code:error.code,stack:error.stack},{status:500})}}`},
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
								if response.StatusCode != 200 || json.Unmarshal(body, &result) != nil || len(result) != 9 {
									t.Errorf("DNS dispatch: %d %s", response.StatusCode, body)
									return
								}
								for index, addresses := range result {
									if index == 7 {
										seen := map[string]int{}
										for _, address := range addresses {
											seen[address.Address] = address.Family
										}
										if len(addresses) != 2 || seen["10.20.30.40"] != 4 || seen["fd00::1"] != 6 {
											t.Errorf("dual-stack DNS: %s", body)
										}
										continue
									}
									wantAddress, wantFamily := "10.20.30.40", 4
									if index == 6 || index == 8 {
										wantAddress, wantFamily = "fd00::1", 6
									}
									if len(addresses) != 1 || addresses[0].Address != wantAddress || addresses[0].Family != wantFamily {
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

type dnsTestTransport func(*http.Request) (*http.Response, error)

func (f dnsTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func dispatchDoHRequest(token, lease string) *http.Request {
	r := httptest.NewRequest("GET", "/internal/runtime/dispatch/outbound", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Nanoflare-Invocation", lease)
	r.Header.Set("X-Nanoflare-Outbound-URL", "https://cloudflare-dns.com/dns-query?name=database.corporate.invalid&type=A&profile=system")
	r.Header.Set("X-Nanoflare-DNS-Profile", "system")
	r.Header.Set("X-Nanoflare-Outbound-Authorization", "caller-secret")
	return r
}

func TestDispatchDNSDefaultProfileAndAdmission(t *testing.T) {
	resolver, queries := privateDNSFixture(t)
	service, token, lease := dnsDispatchLease(t)
	calls := 0
	denyNetwork := dnsTestTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("DNS escaped to egress: %s", r.URL)
		return nil, nil
	})
	transport := dnsresolver.DoHTransport(denyNetwork, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/dns-query" || r.URL.Query().Get("profile") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Nanoflare-DNS-Profile") != "" {
			t.Errorf("caller authority reached resolver: %s %v", r.URL, r.Header)
		}
		resolver.ServeHTTP(w, r)
	}))
	handler := NewRuntimeDispatchServer(service, nil, transport)
	request := func(auth, invocation string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, dispatchDoHRequest(auth, invocation))
		return w
	}
	if w := request("wrong", lease); w.Code == 200 {
		t.Fatal("wrong transport accepted")
	}
	if w := request(token, "nfi1.invalid"); w.Code == 200 {
		t.Fatal("invalid lease accepted")
	}
	if calls != 0 {
		t.Fatal("unauthorized DNS reached resolver")
	}
	for i := 0; i < 100; i++ {
		if w := request(token, lease); w.Code != 200 || !strings.Contains(w.Body.String(), "10.20.30.40") {
			t.Fatalf("lookup %d: %d %s", i, w.Code, w.Body)
		}
	}
	if w := request(token, lease); w.Code != 429 {
		t.Fatalf("effect budget: %d %s", w.Code, w.Body)
	}
	if calls != 100 || queries.Load() == 0 {
		t.Fatalf("resolver calls=%d DNS queries=%d", calls, queries.Load())
	}
	if err := service.FinishDispatch(context.Background(), token, lease, true); err != nil {
		t.Fatal(err)
	}
	if w := request(token, lease); w.Code != 410 {
		t.Fatalf("expired lease: %d %s", w.Code, w.Body)
	}
	if calls != 100 {
		t.Fatal("expired DNS reached resolver")
	}
}

func TestDispatchDNSCancellation(t *testing.T) {
	service, token, lease := dnsDispatchLease(t)
	started, done := make(chan struct{}), make(chan struct{})
	transport := dnsresolver.DoHTransport(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(done)
	}))
	handler := NewRuntimeDispatchServer(service, nil, transport)
	completed := make(chan struct{})
	go func() { handler.ServeHTTP(httptest.NewRecorder(), dispatchDoHRequest(token, lease)); close(completed) }()
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
		t.Fatal("DNS did not cancel with invocation")
	}
	<-completed
}
