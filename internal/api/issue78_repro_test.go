package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

type issue78Transport func(*http.Request) (*http.Response, error)

func (f issue78Transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Regression matrix for #78: diagnostics, redaction, cancellation at the real
// origin, and both local and remote runner transports.
func TestIssue78RuntimeRestrictionObservations(t *testing.T) {
	binary := os.Getenv("NANOFLARE_TEST_WORKERD")
	if binary == "" {
		t.Skip("set NANOFLARE_TEST_WORKERD to production-pinned workerd")
	}
	version, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(version)) != "workerd 2026-09-11" {
		t.Fatalf("requires pinned runtime: %s (%v)", version, err)
	}
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprintf("remote=%t", remote), func(t *testing.T) {
			output := runtime.NewOutputBuffer()
			canceled := make(chan string, 8)
			privateDynamicDispatchWithFixture(t, remote, true, func(server *Server, service *nanoflare.Service, org nanoflare.Organization, _ string) {
				transport := server.workerClient.Transport
				server.workerClient.Transport = issue78Transport(func(request *http.Request) (*http.Response, error) {
					response, err := transport.RoundTrip(request)
					if err != nil {
						return nil, err
					}
					t.Logf("upstream-before-gateway path=%s status=%d", request.URL.RequestURI(), response.StatusCode)
					return response, nil
				})
				must := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				ns, err := service.CreateDispatchNamespace(org.ID, nanoflare.CreateDispatchNamespaceInput{Name: "issue78"})
				must(err)
				customer, err := service.CreateNamespaceScript(org.ID, ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "probe", Execution: "dynamic"})
				must(err)
				child, err := service.CreateNamespaceScript(org.ID, ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "child", Execution: "dynamic"})
				must(err)
				_, err = service.Deploy(child.ID, nanoflare.DeployInput{Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2026-09-11", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {fetch(request){if(new URL(request.url).pathname==='/abort')return fetch('http://8.8.8.8/wait-headers?probe=abort-service',{signal:request.signal});return new Response('child-ok');}};`}}})
				must(err)
				_, err = service.Deploy(customer.ID, nanoflare.DeployInput{Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2026-09-11", Services: []nanoflare.ServiceBinding{{Binding: "CHILD", WorkerID: child.ID}}, Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `
export default {async fetch(request,env,ctx) {
 const url=new URL(request.url), kind=url.pathname.split('/').pop();
 console.log('issue78-enter',kind,url.searchParams.has('caught')?'caught':'uncaught');
 const run=async()=>{
  if(kind==='throw')throw new Error('issue78-deliberate-error');
  if(kind==='cache')return caches.default;
  if(kind==='cf')return fetch('http://8.8.8.8/',{cf:{cacheTtl:60}});
  if(kind==='timeout')return fetch('http://8.8.8.8/',{signal:AbortSignal.timeout(5000)});
  if(kind==='controller')return fetch('http://8.8.8.8/',{signal:new AbortController().signal});
  if(kind==='request-signal')return fetch(new Request('http://8.8.8.8/',{signal:new AbortController().signal}));
  if(kind==='service-plain')return env.CHILD.fetch('https://child.internal/');
  if(kind==='service-signal')return env.CHILD.fetch('https://child.internal/',{signal:new AbortController().signal});
  if(kind==='asset-plain')return env.ASSETS.fetch('/probe.txt');
  if(kind==='asset-signal')return env.ASSETS.fetch('/probe.txt',{signal:new AbortController().signal});
  if(kind==='credentials')return fetch('http://user:pass@8.8.8.8/');
  if(kind==='upgrade')return fetch('http://8.8.8.8/',{headers:{Upgrade:'websocket'}});
  if(kind==='background'){ctx.waitUntil(Promise.reject(new Error('issue78-background-error')));return new Response('background-accepted');}
  if(kind==='background-delayed'){ctx.waitUntil((async()=>{await scheduler.wait(250);throw new Error('issue78-delayed-background-error');})());return new Response('background-accepted');}
  if(kind==='stream')return new Response(new ReadableStream({start(c){c.enqueue(new TextEncoder().encode('first'));},async pull(){await scheduler.wait(50);throw new Error('issue78-stream-error');}}));
  if(kind==='plain')return fetch('http://8.8.8.8/');
  if(kind==='abort-pre')return fetch('http://8.8.8.8/',{signal:AbortSignal.abort(new Error('already-aborted'))});
  if(kind.startsWith('abort-')) {
   const controller=new AbortController();
   const signal=kind==='abort-controller'?controller.signal:AbortSignal.timeout(100);
   if(kind==='abort-controller')setTimeout(()=>controller.abort(new Error('custom-abort')),100);
   const response=kind==='abort-service'?await env.CHILD.fetch('https://child.internal/abort',{signal}):await fetch('http://8.8.8.8/'+(kind==='abort-before'?'wait-headers':kind==='abort-redirect'?'redirect-slow':'slow')+'?probe='+kind,{signal});
   return new Response(await response.text());
  }
  return new Response('ok');
 };
 if(url.searchParams.has('caught')) {
  try{return await run();}catch(error){return Response.json({name:error.name,message:error.message},{status:422});}
 }
 return run();
}};`}}, Assets: []nanoflare.AssetFile{{Path: "probe.txt", Data: []byte("asset-ok")}}, AssetConfig: nanoflare.AssetConfig{RunWorkerFirst: nanoflare.RunWorkerFirst{"/*"}}})
				must(err)
				platform, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "issue78-platform", Hostname: "issue78.test"})
				must(err)
				_, err = service.Deploy(platform.ID, nanoflare.DeployInput{Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2026-09-11", DispatchNamespaces: []nanoflare.DispatchBinding{{Binding: "CUSTOMERS", NamespaceID: ns.ID}}, Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {fetch(request,env){return env.CUSTOMERS.get('probe').fetch(request);}};`}}})
				must(err)
				public := httptest.NewServer(server)
				defer public.Close()
				client := &http.Client{Timeout: 10 * time.Second}
				references := map[string]string{}
				paths := []string{"ok", "plain", "service-plain", "asset-plain", "background", "background-delayed"}
				for _, kind := range []string{"throw", "cf", "cache", "timeout", "controller", "request-signal", "service-signal", "asset-signal", "credentials", "upgrade"} {
					paths = append(paths, kind, kind+"?caught")
				}
				for _, path := range paths {
					response, err := client.Get(public.URL + "/internal/http/workers/" + platform.ID + "/" + path)
					must(err)
					body, err := io.ReadAll(response.Body)
					response.Body.Close()
					must(err)
					headers, _ := json.Marshal(response.Header)
					t.Logf("path=%s status=%d headers=%s body=%q", path, response.StatusCode, headers, body)
					if (path == "ok" || path == "plain" || strings.HasSuffix(path, "-plain") || path == "background") && response.StatusCode != 200 {
						t.Fatalf("baseline failed: %s", body)
					}
					kind := strings.Split(path, "?")[0]
					if !strings.Contains(path, "?") && (kind == "throw" || kind == "cf" || kind == "cache" || kind == "credentials" || kind == "upgrade") {
						reference := response.Header.Get("X-Nanoflare-Error-Id")
						if response.StatusCode != 500 || reference == "" || !strings.Contains(string(body), reference) || strings.Contains(string(body), "TypeError") || strings.Contains(string(body), "issue78-deliberate-error") {
							t.Fatalf("unsafe or uncorrelated execution failure: %d %s", response.StatusCode, body)
						}
						references[path] = reference
					}
					signalSuccess := kind == "timeout" || kind == "controller" || strings.HasSuffix(kind, "-signal")
					if signalSuccess && response.StatusCode != 200 {
						t.Fatalf("signal-bearing fetch failed: %s", body)
					}
					if !signalSuccess && strings.Contains(path, "?caught") {
						var detail struct {
							Name    string
							Message string
						}
						must(json.Unmarshal(body, &detail))
						if response.StatusCode != 422 || detail.Name == "" || detail.Message == "" {
							t.Fatalf("caught exception missing: %s", body)
						}
					}
				}
				// Roots retain admissions for the two-second background window;
				// keep the cancellation matrix separate from the 25 request probes.
				time.Sleep(2100 * time.Millisecond)
				for _, kind := range []string{"abort-pre", "abort-before", "abort-body", "abort-redirect", "abort-service", "abort-controller"} {
					response, err := client.Get(public.URL + "/internal/http/workers/" + platform.ID + "/" + kind + "?caught")
					must(err)
					body, err := io.ReadAll(response.Body)
					response.Body.Close()
					must(err)
					t.Logf("cancellation=%s status=%d body=%s", kind, response.StatusCode, body)
					reason := "TimeoutError"
					if kind == "abort-pre" {
						reason = "already-aborted"
					}
					if kind == "abort-controller" {
						reason = "custom-abort"
					}
					if response.StatusCode != 422 || !strings.Contains(string(body), reason) {
						t.Fatalf("wrong cancellation result: %s", body)
					}
					if kind != "abort-pre" {
						select {
						case got := <-canceled:
							if got != kind {
								t.Fatalf("canceled %s, want %s", got, kind)
							}
						case <-time.After(2 * time.Second):
							t.Fatalf("upstream survived %s", kind)
						}
					}
				}
				response, err := client.Get(public.URL + "/internal/http/workers/" + platform.ID + "/stream")
				must(err)
				streamBody, streamErr := io.ReadAll(response.Body)
				response.Body.Close()
				t.Logf("stream status=%d body=%q read-error=%v", response.StatusCode, streamBody, streamErr)
				broken, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "issue78-broken", Hostname: "broken.issue78.test"})
				must(err)
				must(service.PutSecret(broken.ID, "SECRET", "issue78-frozen-secret"))
				_, err = service.Deploy(broken.ID, nanoflare.DeployInput{Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2026-09-11", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `import {env} from 'cloudflare:workers';throw new Error('issue78-init '+env.SECRET);export default {fetch(){return new Response('unreachable');}};`}}})
				if err == nil {
					t.Fatal("initialization failure was accepted")
				}
				initLogs := output.Output(broken.ID)
				if len(initLogs) != 1 || !strings.Contains(initLogs[0].Message, "issue78-init [redacted]") || initLogs[0].DeploymentID == "" {
					t.Fatalf("initialization log identity/redaction: %#v", initLogs)
				}
				// Console messages verify collection is alive before interpreting an
				// absent exception. Allow asynchronous log RPCs to settle.
				time.Sleep(time.Second)
				lines := output.Output(customer.ID)
				if len(lines) == 0 {
					t.Fatal("no customer console messages were collected")
				}
				for _, line := range lines {
					t.Logf("customer-log level=%s message=%q", line.Level, line.Message)
				}
				for path, reference := range references {
					found := false
					for _, line := range lines {
						found = found || line.Level == "error" && strings.Contains(line.Message, reference) && line.DeploymentID != ""
					}
					if !found {
						t.Errorf("%s: response reference missing from attributed logs: %s", path, reference)
					}
				}
				for _, message := range []string{"issue78-deliberate-error", "Custom fetch/cache options are unavailable", "Cache API is unavailable", "issue78-background-error", "issue78-delayed-background-error", "issue78-stream-error"} {
					found := false
					for _, line := range lines {
						found = found || line.Level == "error" && strings.Contains(line.Message, message)
					}
					if !found {
						t.Errorf("missing attributed exception: %s", message)
					}
				}
				for _, line := range output.Output(platform.ID) {
					t.Logf("platform-log level=%s message=%q", line.Level, line.Message)
				}
			}, dispatchFixtureObservations{Output: output, OutboundCanceled: func(probe string) { canceled <- probe }})
		})
	}
}
