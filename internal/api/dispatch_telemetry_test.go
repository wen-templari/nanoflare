package api

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

func TestDispatchTelemetryScopesPolicyAndTrustedElapsedTime(t *testing.T) {
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
			must(repo.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org", UsageLevel: nanoflare.UsageLevelPaid}))
			buffer := runtime.NewOutputBuffer()
			service := nanoflare.NewServiceWithConsole(repo, discardWriter{}, newAPIObjectStore(), buffer, nil)
			service.SetPlatformDispatchEnabled(true)
			codec, err := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
			must(err)
			service.SetSecretCodec(codec)
			ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "telemetry", CustomerMode: "platform"})
			must(err)
			alpha, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", CustomerID: "a", Execution: "dynamic"})
			must(err)
			child, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "child", CustomerID: "a", Execution: "dynamic"})
			must(err)
			other, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "other", CustomerID: "b", Execution: "dynamic"})
			must(err)
			must(service.PutSecret(alpha.ID, "TOKEN", "long-secret-value"))
			must(service.PutSecret(alpha.ID, "OVERLAP", "long-secret"))
			input := nanoflare.DeployInput{Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}}
			_, err = service.Deploy(child.ID, input)
			must(err)
			_, err = service.Deploy(other.ID, input)
			must(err)
			input.Services = []nanoflare.ServiceBinding{{Binding: "CHILD", WorkerID: child.ID}}
			revision, err := service.Deploy(alpha.ID, input)
			must(err)
			source, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "platform", Hostname: "telemetry.example.com"})
			must(err)
			hostInput := input
			hostInput.Services = nil
			hostInput.DispatchNamespaces = []nanoflare.DispatchBinding{{Binding: "DISPATCHER", NamespaceID: ns.ID}}
			host, err := service.Deploy(source.ID, hostInput)
			must(err)
			active := nanoflare.ActiveDeployment{App: source, Deployment: host}
			token := nanoflare.RuntimeCapability(active, nanoflare.RuntimeDispatch, ns.ID)
			ctx := context.Background()
			root, _, err := service.BeginDispatchRoot(active)
			must(err)
			parent, err := service.AcquireDispatchWithRoot(ctx, token, ns.ID, "alpha", root)
			must(err)
			nested, err := service.AcquireServiceDispatch(ctx, token, parent.Lease, "CHILD")
			must(err)
			separate, err := service.AcquireDispatchWithRoot(ctx, token, ns.ID, "other", root)
			must(err)
			if err = service.AuthorizeDispatchTransport(ctx, "wrong", parent.Lease); !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatal("a different transport acquired log authority")
			}
			must(service.SetTelemetryPolicy(nanoflare.TelemetryPolicy{Enabled: true, SamplePercent: 100, RedactPatterns: []string{`email=[^ ]+`}}))
			line, accepted, err := service.ProcessRuntimeLog(ctx, parent.Lease, "info", "long-secret-value long-secret email=person@example.com nfi1.opaque-handle")
			must(err)
			if !accepted || strings.Contains(line.Message, "secret") || strings.Contains(line.Message, "person@") || strings.Contains(line.Message, "opaque-handle") || line.CustomerID != "a" || line.NamespaceID != ns.ID || line.AppID != alpha.ID || line.DeploymentID != revision.ID || line.RequestChainID == "" || line.InvocationID == "" {
				t.Fatalf("log identity/redaction: %#v", line)
			}
			childLine, accepted, err := service.ProcessRuntimeLog(ctx, nested.Lease, "info", "child")
			must(err)
			if !accepted || childLine.RequestChainID != line.RequestChainID || childLine.ParentInvocationID != line.InvocationID || childLine.InvocationID == line.InvocationID {
				t.Fatalf("nested correlation: %#v %#v", line, childLine)
			}
			otherLine, accepted, err := service.ProcessRuntimeLog(ctx, separate.Lease, "info", "other")
			must(err)
			if !accepted || otherLine.CustomerID != "b" || otherLine.InvocationID == line.InvocationID {
				t.Fatalf("other customer: %#v", otherLine)
			}
			buffer.AppendLine(line)
			buffer.AppendLine(otherLine)
			queried, err := service.WorkerOutputQueryForOrg(ctx, "org", alpha.ID, nanoflare.WorkerOutputQuery{})
			must(err)
			if len(queried) != 1 || queried[0].CustomerID != "a" {
				t.Fatalf("customer output crossed worker route: %#v", queried)
			}
			must(service.SetTelemetryPolicy(nanoflare.TelemetryPolicy{Enabled: true, SamplePercent: 0}))
			_, accepted, err = service.ProcessRuntimeLog(ctx, parent.Lease, "info", "muted")
			must(err)
			if accepted {
				t.Fatal("sampling zero collected a log")
			}
			must(service.SetTelemetryPolicy(nanoflare.TelemetryPolicy{Enabled: true, SamplePercent: 100}))
			_, accepted, err = service.ProcessRuntimeLog(ctx, parent.Lease, "info", strings.Repeat("x", 4097))
			must(err)
			if accepted {
				t.Fatal("oversized log was retained")
			}
			acceptedCount := 1
			for range 100 {
				_, accepted, err = service.ProcessRuntimeLog(ctx, parent.Lease, "info", "bounded")
				must(err)
				if accepted {
					acceptedCount++
				}
			}
			if acceptedCount != 64 || service.RuntimeTelemetryDropped() == 0 {
				t.Fatalf("log quota: %d dropped=%d", acceptedCount, service.RuntimeTelemetryDropped())
			}
			// A noisy customer cannot consume another Worker's collection quota.
			_, accepted, err = service.ProcessRuntimeLog(ctx, separate.Lease, "info", "still allowed")
			must(err)
			if !accepted {
				t.Fatal("noisy worker consumed another worker's quota")
			}
			var mu sync.Mutex
			var events []nanoflare.DispatchObservation
			service.SetDispatchObservationSink(func(event nanoflare.DispatchObservation) {
				_ = service.DispatchCounters(event.WorkerID)
				mu.Lock()
				events = append(events, event)
				mu.Unlock()
			})
			time.Sleep(5 * time.Millisecond)
			must(service.FinishDispatch(ctx, token, nested.Lease, false))
			must(service.FinishDispatch(ctx, token, nested.Lease, false))
			service.EndDispatchRoot(root, true)
			mu.Lock()
			observed := append([]nanoflare.DispatchObservation{}, events...)
			mu.Unlock()
			if len(observed) != 3 {
				t.Fatalf("completion was not observed exactly once: %#v", observed)
			}
			for _, event := range observed {
				if event.ElapsedMS <= 0 || event.ElapsedMS > 30000 || event.RequestChainID != line.RequestChainID {
					t.Fatalf("invalid elapsed/correlation: %#v", event)
				}
			}
			counts := service.DispatchCounters(alpha.ID)
			if counts.Invocations != 1 || counts.Canceled != 1 || counts.Completed != 0 {
				t.Fatalf("wrong invocation counters: %#v", counts)
			}
			counts = service.DispatchCounters(child.ID)
			if counts.Invocations != 1 || counts.Completed != 1 || counts.Canceled != 0 {
				t.Fatalf("completed child counted twice: %#v", counts)
			}
			if _, _, err = service.ProcessRuntimeLog(ctx, parent.Lease, "info", "after cancellation"); !errors.Is(err, nanoflare.ErrInvocationExpired) {
				t.Fatalf("terminal invocation logged: %v", err)
			}
		})
	}
}
