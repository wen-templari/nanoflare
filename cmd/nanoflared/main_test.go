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
