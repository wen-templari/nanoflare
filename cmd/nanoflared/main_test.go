package main

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

func TestCompatibilityFlagRequiresNativePilot(t *testing.T) {
	if os.Getenv("NANOFLARE_TEST_FEATURE_FLAG_CHILD") == "1" {
		flag.CommandLine = flag.NewFlagSet("nanoflared", flag.ExitOnError)
		os.Args = []string{"nanoflared", "--enable-cloudflare-compatibility"}
		main()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCompatibilityFlagRequiresNativePilot$")
	command.Dir = t.TempDir() // No project .env, production state or credentials.
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "NANOFLARE_TEST_FEATURE_FLAG_CHILD=1"}
	output, err := command.CombinedOutput()
	if ctx.Err() != nil || err == nil || !strings.Contains(string(output), "Cloudflare compatibility requires --enable-platform-dispatch") {
		t.Fatalf("adapter flag did not reject missing native pilot before startup: %v %s", err, output)
	}
}

func TestRuntimeMuxRoutesObjectRequestsToObjectHandler(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewServiceWithObjects(store, config.NewWriter("", "", "", ""), nil)
	server := api.NewServer(service)
	runtimeMux := newRuntimeMux(service, server, runtime.NewDurationTelemetry())
	defer runtimeMux.Close()

	request := httptest.NewRequest(http.MethodGet, "/internal/runtime/objects/demo.txt", nil)
	recorder := httptest.NewRecorder()
	runtimeMux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "invalid runtime capability") {
		t.Fatalf("body = %q, want authentication error", recorder.Body.String())
	}
}

func TestRuntimeExecutionControlIsPrivateAndAuthenticated(t *testing.T) {
	service := nanoflare.NewService(nanoflare.NewStore(), config.NewWriter("", "", "", ""))
	public := api.NewServer(service)
	private := newRuntimeMuxWithControl(service, public, runtime.NewDurationTelemetry(), nil, "runner-control")
	defer private.Close()
	for _, token := range []string{"", "wrong", "runner-control"} {
		request := httptest.NewRequest(http.MethodPost, "/internal/runtime/executions/open-session", strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Nanoflare-Runner-Token", token)
		response := httptest.NewRecorder()
		private.ServeHTTP(response, request)
		if token != "runner-control" && response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated private control status = %d", response.Code)
		}
		if token == "runner-control" && response.Code != http.StatusOK {
			t.Fatalf("authenticated private control status = %d: %s", response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/runtime/executions/open-session", strings.NewReader(`{}`))
	request.Header.Set("X-Nanoflare-Runner-Token", "runner-control")
	response := httptest.NewRecorder()
	public.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("execution control exposed publicly: %d", response.Code)
	}
}

func TestRuntimeResourcePrefixPreservesReservedKVKeys(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, config.NewWriter("", "", "", ""))
	kv, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{Name: "reserved-paths"})
	if err != nil {
		t.Fatal(err)
	}
	private := newRuntimeMuxWithControl(service, api.NewServer(service), runtime.NewDurationTelemetry(), nil, "runner-control")
	defer private.Close()
	app, err := service.CreateApp(nanoflare.CreateAppInput{Name: "reserved-worker", Hostname: "reserved.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := service.RegisterExecution(nanoflare.ActiveDeployment{App: app, Deployment: nanoflare.Deployment{ID: "reserved-rev", AppID: app.ID, KVNamespaces: []nanoflare.KVBinding{{Binding: "KV", ID: kv.ID}}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := service.Executions().Admit(id)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	for _, key := range []string{"internal/runtime/durations", "internal/runtime/executions/open-session", "internal/runtime/objects/demo.txt"} {
		if err := store.KVPut("", kv.ID, key, []byte("resource-value")); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodGet, "/internal/runtime/resources/"+key+"?urlencoded=true", nil)
		request.Header.Set("X-Nanoflare-KV-Namespace-ID", kv.ID)
		request.Header.Set("Authorization", "Bearer "+lease.Token())
		response := httptest.NewRecorder()
		private.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != "resource-value" {
			t.Fatalf("reserved KV key %q: HTTP %d %s", key, response.Code, response.Body.String())
		}
	}
}

func TestPlatformDNSRemoteConfiguration(t *testing.T) {
	if server, _, err := platformDNS("invalid-json", "not-an-address", true, false); err != nil || server != nil {
		t.Fatalf("unused remote DNS config: %v", err)
	}
	if _, _, err := platformDNS("", "0.0.0.0:8083", true, true); err != nil {
		t.Fatalf("remote DNS validated an unused listen address: %v", err)
	}
	if _, _, err := platformDNS("invalid-json", "127.0.0.1:0", true, true); err == nil {
		t.Fatal("enabled remote WFP DNS accepted invalid config")
	}
	if _, _, err := platformDNS("", "0.0.0.0:8083", false, false); err == nil {
		t.Fatal("local adapter accepted non-loopback listener")
	}
}
