package api

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestCloudflareSDKRuntimeWorkflow(t *testing.T) {
	cloudflareSDKRuntimeWorkflow(t)
}

func TestCloudflareSDKRuntimePostgresWorkflow(t *testing.T) {
	if os.Getenv("NANOFLARE_TEST_POSTGRES_URL") == "" {
		t.Skip("disposable PostgreSQL required")
	}
	t.Setenv("NANOFLARE_TEST_RUNTIME_BACKEND", "postgres")
	cloudflareSDKRuntimeWorkflow(t)
}

func cloudflareSDKRuntimeWorkflow(t *testing.T) {
	sdk := os.Getenv("NANOFLARE_TEST_CF_SDK_DIR")
	if sdk == "" || os.Getenv("NANOFLARE_TEST_WORKERD") == "" {
		t.Skip("set pinned SDK directory and native NANOFLARE_TEST_WORKERD executable")
	}
	if !filepath.IsAbs(sdk) {
		t.Fatal("SDK directory must be absolute")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	privateDynamicDispatchWithFixture(t, false, true, func(handler *Server, service *nanoflare.Service, org nanoflare.Organization, token string) {
		handler.SetCloudflareCompatibilityEnabled(true)
		server := httptest.NewServer(handler)
		defer server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, node, "../../scripts/wfp-client-flows/sdk-runtime.mjs")
		command.Env = append(os.Environ(), "NF_SDK_FIXTURE_URL="+server.URL, "NF_SDK_FIXTURE_TOKEN="+token, "NF_SDK_FIXTURE_ORG="+org.ID)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("SDK runtime: %v\n%s", err, output)
		}
		t.Log(string(output))
		namespaces, err := service.DispatchNamespacesForOrg(org.ID)
		if err != nil || len(namespaces) != 0 {
			t.Fatalf("namespace cleanup: %v %v", namespaces, err)
		}
		worker, err := service.OrdinaryScriptForOrg(org.ID, "sdk-dispatcher")
		if err != nil {
			t.Fatal(err)
		}
		if err := service.DeleteAppForOrg(org.ID, worker.ID); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCloudflareSDKClientFlows(t *testing.T) {
	sdk := os.Getenv("NANOFLARE_TEST_CF_SDK_DIR")
	if sdk == "" {
		t.Skip("set NANOFLARE_TEST_CF_SDK_DIR to an isolated cloudflare@7.3.0 package directory")
	}
	if !filepath.IsAbs(sdk) {
		t.Fatal("SDK directory must be absolute")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var repo nanoflare.Repository = nanoflare.NewStore()
			if backend == "postgres" {
				repo = runtimePostgres(t)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			auth := nanoflare.NewControlAuthService(repo, "sdk-test")
			auth.SetTestHashCost(4)
			session, err := auth.Signup(nanoflare.SignupInput{Email: "sdk@example.com", Password: "password"})
			must(err)
			org, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "sdk"})
			must(err)
			service := nanoflare.NewServiceWithObjects(repo, discardWriter{}, newAPIObjectStore())
			service.SetPlatformDispatchEnabled(true)
			codec, err := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
			must(err)
			service.SetSecretCodec(codec)
			handler := NewServerWithControlAuth(service, nil, "", nil, auth)
			handler.SetCloudflareCompatibilityEnabled(true)
			server := httptest.NewServer(handler)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, node, "../../scripts/wfp-client-flows/sdk.mjs")
			command.Env = append(os.Environ(), "NF_SDK_FIXTURE_URL="+server.URL, "NF_SDK_FIXTURE_TOKEN="+session.Token, "NF_SDK_FIXTURE_ORG="+org.ID)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("SDK flow: %v\n%s", err, output)
			}
			t.Log(string(output))
			remaining, err := service.DispatchNamespacesForOrg(org.ID)
			must(err)
			if len(remaining) != 0 {
				t.Fatal("SDK fixture did not delete its namespace")
			}
		})
	}
}
