package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestOutboundOrdinaryAPIs(t *testing.T) {
	if os.Getenv("NANOFLARE_TEST_WORKERD") == "" {
		t.Skip("pinned native workerd required")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var dials atomic.Int32
	go func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			dials.Add(1)
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			_, _ = io.WriteString(conn, "local-socket-ok")
			conn.Close()
		}
	}()
	for _, dynamic := range []bool{false, true} {
		for _, remote := range []bool{false, true} {
			t.Run(fmt.Sprintf("dynamic=%t/remote=%t", dynamic, remote), func(t *testing.T) {
				privateDynamicDispatchWithFixture(t, remote, dynamic, func(server *Server, s *nanoflare.Service, org nanoflare.Organization, _ string) {
					must := func(err error) {
						t.Helper()
						if err != nil {
							t.Fatal(err)
						}
					}
					ns, e := s.CreateDispatchNamespace(org.ID, nanoflare.CreateDispatchNamespaceInput{Name: "ordinary-apis"})
					must(e)
					customer, e := s.CreateNamespaceScript(org.ID, ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "customer", Execution: "dynamic"})
					must(e)
					deploy := func(id, source string, bindings []nanoflare.DispatchBinding) {
						t.Helper()
						_, e := s.Deploy(id, nanoflare.DeployInput{Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2026-09-11", CompatibilityFlags: []string{"nodejs_compat"}, Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: source}}, DispatchNamespaces: bindings})
						must(e)
					}
					customerCode := fmt.Sprintf(`import {connect} from 'cloudflare:sockets';export default {async fetch(request){if(new URL(request.url).searchParams.has('customer-tcp')){try{const socket=connect(%q);await socket.opened;const text=await new Response(socket.readable).text();return new Response(text)}catch{return new Response('customer-tcp-denied',{status:403})}}return fetch('http://8.8.8.8/ordinary-api');}};`, listener.Addr().String())
					deploy(customer.ID, customerCode, nil)
					policy, e := s.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "ordinary-api-policy", Hostname: "api-policy.test"})
					must(e)
					// Exactly the same module is invoked through ordinary ingress and as policy.
					source := fmt.Sprintf(`import {connect} from 'cloudflare:sockets';export default {async fetch(request,env){const result={parameter:env.context?.customer??null};try{const socket=connect(%q);await socket.opened;result.tcp=await new Response(socket.readable).text();await socket.close()}catch(error){result.tcp={name:error.name,message:error.message}}
 async function probe(cache){const key='https://cache-key.invalid/'+crypto.randomUUID();try{await cache.put(key,new Response('cached-body',{headers:{'cache-control':'max-age=60'}}));const matched=await cache.match(key);const body=matched?await matched.text():null;const deleted=await cache.delete(key);return {body,deleted}}catch(error){return {name:error.name,message:error.message}}}
 result.cacheDefault=await probe(caches.default);try{result.cacheNamed=await probe(await caches.open('ordinary-api-parity'))}catch(error){result.cacheNamed={name:error.name,message:error.message}}return Response.json(result)}};`, listener.Addr().String())
					deploy(policy.ID, source, nil)
					host, e := s.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "ordinary-api-host", Hostname: "api-host.test"})
					must(e)
					deploy(host.ID, `export default {fetch(request,env){return env.CUSTOMERS.get('customer',{}, {outbound:{context:{customer:'policy-parameter'}}}).fetch(request)}}`, []nanoflare.DispatchBinding{{Binding: "CUSTOMERS", NamespaceID: ns.ID, Outbound: &nanoflare.DispatchOutbound{WorkerID: policy.ID, Parameters: []string{"context"}}}})
					public := httptest.NewServer(server)
					defer public.Close()
					get := func(id, query string) (int, []byte) {
						t.Helper()
						response, e := public.Client().Get(public.URL + "/internal/http/workers/" + id + "/?" + query)
						must(e)
						defer response.Body.Close()
						body, e := io.ReadAll(response.Body)
						must(e)
						return response.StatusCode, body
					}
					decode := func(id string) map[string]any {
						t.Helper()
						status, body := get(id, "")
						if status != 200 {
							t.Fatalf("worker %s: %d %s", id, status, body)
						}
						var result map[string]any
						must(json.Unmarshal(body, &result))
						return result
					}
					ordinary := decode(policy.ID)
					outbound := decode(host.ID)
					if ordinary["tcp"] != "local-socket-ok" || outbound["tcp"] != "local-socket-ok" {
						t.Fatalf("ordinary target TCP lost: ordinary=%v outbound=%v", ordinary, outbound)
					}
					if ordinary["parameter"] != nil || outbound["parameter"] != "policy-parameter" {
						t.Fatalf("parameter overlay: ordinary=%v outbound=%v", ordinary, outbound)
					}
					for _, name := range []string{"cacheDefault", "cacheNamed"} {
						if !reflect.DeepEqual(ordinary[name], outbound[name]) {
							t.Fatalf("%s ordinary/outbound mismatch: ordinary=%v outbound=%v", name, ordinary[name], outbound[name])
						}
						t.Logf("%s baseline and outbound: %v", name, outbound[name])
					}
					before := dials.Load()
					status, body := get(host.ID, "customer-tcp=1")
					if status != 403 || string(body) != "customer-tcp-denied" {
						t.Fatalf("customer TCP: %d %s", status, body)
					}
					if dials.Load() != before {
						t.Fatal("policy-bound customer dialed local socket")
					}
				}, dispatchFixtureObservations{ConfigureWriter: func(writer *config.RuntimeWriter) { writer.SetNetworkAllow([]string{"local"}) }})
			})
		}
	}
}
