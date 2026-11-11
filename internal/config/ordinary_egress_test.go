package config

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/egress"
	"github.com/clas/nanoflare/internal/nanoflare"
)

// Exercise the production config and shared compiler, not Miniflare's direct
// network default. A proxy-only hostname makes accidental fetch bypass fail.
func TestWorkerdOrdinarySocketTLS(t *testing.T) {
	binary := os.Getenv("NANOFLARE_TEST_WORKERD")
	if binary == "" {
		t.Skip("set NANOFLARE_TEST_WORKERD to pinned workerd")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"database.invalid"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	certificate := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	listen := func(mode string) string {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { listener.Close() })
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				go func() {
					defer conn.Close()
					conn.SetDeadline(time.Now().Add(5 * time.Second))
					var stream net.Conn = conn
					if mode == "starttls" {
						var command [1]byte
						if _, err := io.ReadFull(conn, command[:]); err != nil {
							return
						}
						if command[0] != 'S' {
							return
						}
						if _, err := conn.Write([]byte("S")); err != nil {
							return
						}
					}
					if mode != "plain" {
						stream = tls.Server(conn, tlsConfig)
					}
					io.Copy(stream, stream)
				}()
			}
		}()
		_, port, _ := net.SplitHostPort(listener.Addr().String())
		return port
	}
	plain, immediate, upgrade := listen("plain"), listen("tls"), listen("starttls")
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "proxy-only.invalid" {
			t.Errorf("unexpected proxy target: %s", r.URL)
		}
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "http://proxy-only.invalid/final")
			w.WriteHeader(302)
			return
		}
		io.WriteString(w, "proxied:"+r.URL.Path)
	}))
	defer proxy.Close()
	adapter, err := egress.New(egress.Config{Addr: "127.0.0.1:0", ProxyURL: proxy.URL, ProxyResolves: true, NetworkAllow: []string{"public", "local"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		adapter.Close(ctx)
	}()
	operations := `const capturedFetch=globalThis.fetch;
async function run(request) {
 const url=new URL(request.url),mode=url.searchParams.get('mode');
 try {
  if(mode==='fetch') return new Response(await (await capturedFetch('http://proxy-only.invalid/final')).text());
  if(mode==='redirect') return new Response(await (await capturedFetch('http://proxy-only.invalid/redirect')).text());
  if(mode==='manual') {const r=await capturedFetch(new Request('http://proxy-only.invalid/redirect',{redirect:'manual'}));return new Response(String(r.status));}
  if(mode==='abort') {const c=new AbortController();c.abort();try{await capturedFetch('http://proxy-only.invalid/final',{signal:c.signal});return new Response('not aborted',{status:500});}catch{return new Response('aborted');}}
  const hostname=url.searchParams.has('wrong')?'localhost':'127.0.0.1';
  const port=mode==='plain'?PLAIN:mode==='starttls'?UPGRADE:IMMEDIATE;
  if(mode==='node-tls') {
   const socket=tls.connect({host:hostname,port});
   try {await new Promise((resolve,reject)=>{socket.once('secureConnect',resolve);socket.once('error',reject)});const data=new Promise((resolve,reject)=>{socket.once('data',resolve);socket.once('error',reject)});socket.write('hello');return new Response(String(await data));}finally{socket.destroy()}
  }
  let socket=connect({hostname,port},{secureTransport:mode==='plain'?'off':mode==='starttls'?'starttls':'on'});
  try {
   await socket.opened;
   if(mode==='starttls') {const w=socket.writable.getWriter();await w.write(new TextEncoder().encode('S'));w.releaseLock();const r=socket.readable.getReader();await r.read();r.releaseLock();socket=socket.startTls();await socket.opened;}
   const writer=socket.writable.getWriter();await writer.write(new TextEncoder().encode('hello'));writer.releaseLock();const reader=socket.readable.getReader();const data=await reader.read();reader.releaseLock();return new Response(new TextDecoder().decode(data.value));
  }finally {await socket.close();}
 }catch(error){return new Response(String(error),{status:502});}
}
`
	operations = strings.NewReplacer("PLAIN", plain, "UPGRADE", upgrade, "IMMEDIATE", immediate).Replace(operations)
	for _, execution := range []string{"static-module", "static-class", "static-no-env", "dynamic-module", "dynamic-class", "dynamic-legacy", "dynamic-no-env"} {
		for _, policy := range []string{"allowed", "denied", "untrusted"} {
			t.Run(execution+"/"+policy, func(t *testing.T) {
				options := WorkerdOptions{NetworkAllow: []string{"local"}, NetworkCertificates: []string{certPEM}, EgressAddr: adapter.Addr()}
				if policy == "denied" {
					options.NetworkAllow = []string{"192.0.2.0/24"}
				}
				if policy == "untrusted" {
					options.NetworkCertificates = nil
				}
				source := `import {connect} from 'cloudflare:sockets';import tls from 'node:tls';` + operations
				format := "modules"
				if strings.HasSuffix(execution, "class") {
					source += `import {WorkerEntrypoint} from 'cloudflare:workers';export default class extends WorkerEntrypoint {fetch(request){return run(request)}}`
				} else if strings.HasSuffix(execution, "legacy") {
					format = "service-worker"
					source += `addEventListener('fetch',event=>event.respondWith(run(event.request)));`
				} else {
					source += `export default {fetch:run};`
				}
				active := nanoflare.ActiveDeployment{App: nanoflare.App{ID: "ordinary"}, Deployment: nanoflare.Deployment{ID: "v1", Format: format, Entrypoint: "worker.js", CompatibilityDate: "2026-09-08", CompatibilityFlags: []string{"nodejs_compat"}, Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: source}}}}
				if strings.HasSuffix(execution, "no-env") {
					active.Deployment.CompatibilityFlags = append(active.Deployment.CompatibilityFlags, "disallow_importable_env")
				}
				dir := t.TempDir()
				socket := filepath.Join(dir, "http.sock")
				active.SocketPath = socket
				generated := WorkerdWithOptions([]nanoflare.ActiveDeployment{active}, options)
				dynamic := strings.HasPrefix(execution, "dynamic")
				if dynamic {
					code := InstrumentLoaderInvocation(SnapshotWorkerCodeForLoader(active, options), active)
					wire, err := json.Marshal(code)
					if err != nil {
						t.Fatal(err)
					}
					var program map[string]any
					if err := json.Unmarshal(wire, &program); err != nil {
						t.Fatal(err)
					}
					program["env"] = map[string]any{"__NANOFLARE_APP_ID": "ordinary"}
					broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/source/ordinary" {
							json.NewEncoder(w).Encode(program)
							return
						}
						w.WriteHeader(204)
					}))
					defer broker.Close()
					generated = DynamicDispatcher(options, socket, broker.Listener.Addr().String(), "broker", "dispatcher")
				}
				path := filepath.Join(dir, "workerd.capnp")
				if err := os.WriteFile(path, []byte(generated), 0600); err != nil {
					t.Fatal(err)
				}
				logs, err := os.Create(filepath.Join(dir, "runtime.log"))
				if err != nil {
					t.Fatal(err)
				}
				defer logs.Close()
				command := exec.Command(binary, "serve", "--experimental", path)
				command.Stdout = logs
				command.Stderr = logs
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() { command.Process.Kill(); command.Wait() }()
				transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socket)
				}}
				defer transport.CloseIdleConnections()
				client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
				call := func(query string) (int, string, error) {
					request, _ := http.NewRequest("GET", "http://ordinary/?"+query, nil)
					if dynamic {
						request.Header.Set("X-Nanoflare-Dispatcher-Token", "dispatcher")
						request.Header.Set("X-Nanoflare-Execution", "ordinary")
						request.Header.Set("X-Nanoflare-Invocation", "test")
					}
					response, err := client.Do(request)
					if err != nil {
						return 0, "", err
					}
					defer response.Body.Close()
					data, err := io.ReadAll(response.Body)
					return response.StatusCode, string(data), err
				}
				ready := false
				for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
					if _, err := os.Stat(socket); err == nil {
						ready = true
						break
					}
				}
				if !ready {
					data, _ := os.ReadFile(logs.Name())
					t.Fatalf("workerd did not start: %s", data)
				}
				for _, mode := range []string{"tls", "starttls", "plain", "node-tls", "fetch", "redirect", "manual", "abort"} {
					status, body, err := call("mode=" + mode)
					success := policy == "allowed" || mode == "fetch" || mode == "redirect" || mode == "manual" || mode == "abort" || (policy == "untrusted" && mode == "plain")
					want := 200
					if !success {
						want = 502
					}
					if err != nil || status != want {
						data, _ := os.ReadFile(logs.Name())
						t.Fatalf("%s: status=%d body=%s err=%v\n%s", mode, status, body, err, data)
					}
					if success {
						expected := "hello"
						switch mode {
						case "fetch", "redirect":
							expected = "proxied:/final"
						case "manual":
							expected = "302"
						case "abort":
							expected = "aborted"
						}
						if body != expected {
							t.Fatalf("%s: %q, want %q", mode, body, expected)
						}
					}
				}
				if policy == "allowed" {
					for _, mode := range []string{"tls", "starttls", "node-tls"} {
						status, body, err := call("mode=" + mode + "&wrong=1")
						if err != nil || status != 502 {
							t.Fatalf("wrong hostname %s: %d %s %v", mode, status, body, err)
						}
					}
				}
			})
		}
	}
}

func TestOrdinaryNetworkCertificatesSnapshot(t *testing.T) {
	for _, writer := range []interface {
		SetNetworkCertificates([]string)
		RuntimeOptions() WorkerdOptions
	}{NewWriter("", "", "", ""), NewRuntimeWriter("", nil)} {
		certificates := []string{"test-pem"}
		writer.SetNetworkCertificates(certificates)
		certificates[0] = "changed"
		snapshot := writer.RuntimeOptions()
		snapshot.NetworkCertificates[0] = "changed"
		if writer.RuntimeOptions().NetworkCertificates[0] != "test-pem" {
			t.Fatal("network trust store aliases caller state")
		}
	}
}
