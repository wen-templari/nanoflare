package main

import (
	"context"
	"crypto/rand"
	"encoding/json"

	"flag"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/database"
	"github.com/clas/nanoflare/internal/dnsresolver"
	"github.com/clas/nanoflare/internal/egress"
	"github.com/clas/nanoflare/internal/logs"
	"github.com/clas/nanoflare/internal/metrics"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/objects"
	"github.com/clas/nanoflare/internal/oidc"
	"github.com/clas/nanoflare/internal/runner"
	"github.com/clas/nanoflare/internal/runtime"
	"github.com/clas/nanoflare/internal/telemetry"
)

type runtimePublisher interface {
	Write([]nanoflare.ActiveDeployment) error
}

func main() {
	if err := loadEnvFile(".env"); err != nil {
		log.Fatal(err)
	}

	var (
		wfpSocketAllow                 = flag.String("wfp-socket-allow", envOrDefault("NANOFLARE_WFP_SOCKET_ALLOW", ""), "opt-in WFP socket destination allow list (including explicit public)")
		wfpSocketCAFiles               = flag.String("wfp-socket-ca-files", envOrDefault("NANOFLARE_WFP_SOCKET_CA_FILES", ""), "comma-separated PEM CA files for WFP sockets")
		platformDispatchEnabled        = flag.Bool("enable-platform-dispatch", envBoolOrDefault("NANOFLARE_PLATFORM_DISPATCH_ENABLED", false), "administrator opt-in to trusted_shared_workerd dispatch; namespaces share a runtime without CPU interruption or hardened untrusted isolation")
		cloudflareCompatibilityEnabled = flag.Bool("enable-cloudflare-compatibility", envBoolOrDefault("NANOFLARE_CLOUDFLARE_COMPATIBILITY_ENABLED", false), "enable the experimental trusted Cloudflare management adapter; requires platform dispatch")
		addr                           = flag.String("addr", ":8080", "HTTP listen address")
		runtimeAddr                    = flag.String("runtime-addr", "127.0.0.1:8081", "private runtime KV API listen address")
		configDir                      = flag.String("config-dir", "./var/generated", "directory for generated runtime configuration")
		dbDir                          = flag.String("db-dir", "", "directory for nanoflare SQLite databases; defaults to <config-dir>/db")
		litestreamEnabled              = flag.Bool("litestream-enabled", false, "enable Litestream restore and replication for SQLite databases")
		litestreamBin                  = flag.String("litestream-bin", "litestream", "path to the litestream executable")
		litestreamConfig               = flag.String("litestream-config", "", "optional Litestream config file")
		traefikFile                    = flag.String("traefik-file", "", "optional Traefik dynamic configuration file fallback")
		authURL                        = flag.String("auth-url", "http://host.docker.internal:8080/internal/auth/verify", "Traefik ForwardAuth callback URL")
		workerHost                     = flag.String("worker-host", "host.docker.internal", "hostname Traefik uses to reach workerd sockets")
		baseHostname                   = flag.String("base-hostname", os.Getenv("NANOFLARE_BASE_HOSTNAME"), "base DNS hostname used when worker hostnames are omitted")
		workerd                        = flag.String("workerd", envOrDefault("NANOFLARE_WORKERD_EXECUTABLE", "workerd"), "path to the workerd executable")
		workerdNetworkAllow            = flag.String("workerd-network-allow", envOrDefault("NANOFLARE_WORKERD_NETWORK_ALLOW", strings.Join(config.DefaultNetworkAllow(), ",")), "comma-separated workerd outbound network allow list")
		egressProxyURL                 = flag.String("workerd-egress-proxy-url", os.Getenv("NANOFLARE_WORKERD_EGRESS_PROXY_URL"), "explicit HTTP proxy URL for Worker outbound traffic")
		egressProxyResolves            = flag.Bool("workerd-egress-proxy-resolves", strings.EqualFold(os.Getenv("NANOFLARE_WORKERD_EGRESS_PROXY_RESOLVES"), "true"), "trust selected egress proxy to resolve names when local DNS fails")
		egressCAFiles                  = flag.String("workerd-egress-ca-files", os.Getenv("NANOFLARE_WORKERD_EGRESS_CA_FILES"), "comma-separated corporate CA PEM files")
		egressNoProxy                  = flag.String("workerd-egress-no-proxy", os.Getenv("NANOFLARE_WORKERD_EGRESS_NO_PROXY"), "comma-separated Worker destinations that bypass the egress proxy")
		egressAddr                     = flag.String("workerd-egress-addr", envOrDefault("NANOFLARE_WORKERD_EGRESS_ADDR", "127.0.0.1:8082"), "private Worker egress adapter address")
		dnsConfigValue                 = flag.String("dns-config", os.Getenv("NANOFLARE_DNS_CONFIG"), "inline JSON or path to the platform DNS profile configuration")
		dnsAddr                        = flag.String("dns-addr", envOrDefault("NANOFLARE_DNS_ADDR", "127.0.0.1:8083"), "private Worker DNS adapter address")
		socketDir                      = flag.String("runtime-socket-dir", "/tmp/nanoflare-sockets", "private directory for Worker Unix sockets")
		prometheus                     = flag.String("prometheus-url", "http://127.0.0.1:9090", "Prometheus base URL for worker traffic metrics")
		lokiURL                        = flag.String("loki-url", os.Getenv("NANOFLARE_LOKI_URL"), "Loki base URL for persistent worker output")
		metricsToken                   = flag.String("metrics-token", os.Getenv("NANOFLARE_METRICS_TOKEN"), "operator bearer credential for the global Prometheus exporter; empty disables it")
		workerLogsEnabled              = flag.Bool("worker-logs-enabled", true, "Collect attributed Worker logs")
		workerLogSample                = flag.Int("worker-log-sample-percent", 100, "Attributed Worker log sample percentage (0-100)")
		workerLogPatterns              = flag.String("worker-log-redact-patterns", "[]", "JSON array of extra log redaction regular expressions; known secret values are always redacted")
		vectorSocket                   = flag.String("log-vector-socket", os.Getenv("NANOFLARE_LOG_VECTOR_SOCKET"), "optional local Vector Unix socket for structured runtime output")
		runnerURL                      = flag.String("runner-url", "", "nanoflare-runner control API URL; empty starts workerd directly")
		runnerToken                    = flag.String("runner-token", os.Getenv("NANOFLARE_RUNNER_TOKEN"), "nanoflare-runner authentication token")
		traefikToken                   = flag.String("traefik-token", os.Getenv("NANOFLARE_TRAEFIK_TOKEN"), "Traefik HTTP provider authentication token")
		authSecret                     = flag.String("control-auth-secret", envOrDefault("NANOFLARE_AUTH_SECRET", "nanoflare-development-control-secret"), "JWT signing secret for control-plane email/password auth")
		oidcIssuer                     = flag.String("oidc-issuer", os.Getenv("NANOFLARE_OIDC_ISSUER"), "OIDC issuer URL for protected worker routes")
		oidcAudience                   = flag.String("oidc-audience", os.Getenv("NANOFLARE_OIDC_AUDIENCE"), "OIDC audience expected in protected worker JWTs")
		oidcEmail                      = flag.String("oidc-email-claim", envOrDefault("NANOFLARE_OIDC_EMAIL_CLAIM", "email"), "OIDC email claim fallback used when userinfo omits email")
		oidcClientID                   = flag.String("oidc-client-id", os.Getenv("NANOFLARE_OIDC_CLIENT_ID"), "OIDC client ID for browser login flow; defaults to oidc-audience when omitted")
		oidcSecret                     = flag.String("oidc-client-secret", os.Getenv("NANOFLARE_OIDC_CLIENT_SECRET"), "OIDC client secret for browser login flow")
		oidcPublic                     = flag.String("oidc-public-url", os.Getenv("NANOFLARE_OIDC_PUBLIC_URL"), "Public control-plane base URL for browser login callback routing, for example https://nanoflare.example.com:8443")
		oidcCookie                     = flag.String("oidc-cookie-domain", os.Getenv("NANOFLARE_OIDC_COOKIE_DOMAIN"), "Optional parent cookie domain shared by the callback host and worker hosts, for example .local.nbtca.space")
		controlOIDCIssuer              = flag.String("control-oidc-issuer", os.Getenv("NANOFLARE_CONTROL_OIDC_ISSUER"), "OIDC issuer URL for console login")
		controlOIDCEmail               = flag.String("control-oidc-email-claim", envOrDefault("NANOFLARE_CONTROL_OIDC_EMAIL_CLAIM", "email"), "OIDC email claim used when userinfo omits email during console login")
		controlOIDCClientID            = flag.String("control-oidc-client-id", os.Getenv("NANOFLARE_CONTROL_OIDC_CLIENT_ID"), "OIDC client ID for console login")
		controlOIDCSecret              = flag.String("control-oidc-client-secret", os.Getenv("NANOFLARE_CONTROL_OIDC_CLIENT_SECRET"), "OIDC client secret for console login")
		controlOIDCPublic              = flag.String("control-oidc-public-url", os.Getenv("NANOFLARE_CONTROL_OIDC_PUBLIC_URL"), "Public console base URL for OIDC login callbacks, for example https://console.example.com")
		controlOIDCDirect              = flag.Bool("control-oidc-direct-login", envBoolOrDefault("NANOFLARE_CONTROL_OIDC_DIRECT_LOGIN", false), "start console OIDC login directly instead of showing the login form first")
		otelEndpoint                   = flag.String("otel-endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), "optional OTLP/gRPC endpoint URL for distributed traces")
		otelSampleRatio                = flag.Float64("otel-sample-ratio", envFloatOrDefault("NANOFLARE_OTEL_SAMPLE_RATIO", 0.1), "fraction of root traces to sample, from 0 to 1")
	)
	flag.Parse()
	wfpSockets, err := config.LoadWFPSocketOptions(*wfpSocketAllow, *wfpSocketCAFiles)
	if err != nil {
		log.Fatal(err)
	}
	if *cloudflareCompatibilityEnabled && !*platformDispatchEnabled {
		log.Fatal("Cloudflare compatibility requires --enable-platform-dispatch (or NANOFLARE_PLATFORM_DISPATCH_ENABLED=true)")
	}

	if *platformDispatchEnabled {
		log.Print("Workers for Platforms enabled: trusted_shared_workerd; namespaces share a runtime failure boundary, with no CPU interruption or hardened untrusted isolation")
	}

	if *traefikFile == "" && *traefikToken == "" {
		log.Fatal("Traefik token is required when HTTP discovery is enabled")
	}
	if err := os.MkdirAll(*configDir, 0o700); err != nil {
		log.Fatal(err)
	}
	if err := os.Chmod(*configDir, 0o700); err != nil {
		log.Fatal(err)
	}
	if *dbDir == "" {
		*dbDir = filepath.Join(*configDir, "db")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownTracing, err := telemetry.ConfigureTracing(ctx, telemetry.TracingConfig{
		Endpoint:    *otelEndpoint,
		ServiceName: envOrDefault("OTEL_SERVICE_NAME", "nanoflared"),
		SampleRatio: *otelSampleRatio,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(ctx); err != nil {
			log.Printf("shut down OpenTelemetry tracing: %v", err)
		}
	}()
	if strings.TrimSpace(*otelEndpoint) != "" {
		log.Printf("exporting OpenTelemetry traces to %s", *otelEndpoint)
	}

	var store nanoflare.Repository = nanoflare.NewStore()
	var objectStore nanoflare.ObjectStore
	if endpoint := os.Getenv("MINIO_ENDPOINT"); endpoint != "" {
		secure, err := strconv.ParseBool(envOrDefault("MINIO_SECURE", "false"))
		if err != nil {
			log.Fatal(err)
		}
		objectStore, err = objects.Open(ctx, objects.MinIOConfig{
			Endpoint:  endpoint,
			AccessKey: os.Getenv("MINIO_ACCESS_KEY"),
			SecretKey: os.Getenv("MINIO_SECRET_KEY"),
			Bucket:    envOrDefault("MINIO_BUCKET", "nanoflare"),
			Secure:    secure,
		})
		if err != nil {
			log.Fatal(err)
		}
		log.Print("using MinIO object store")
	}
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		postgres, err := database.Open(ctx, databaseURL)
		if err != nil {
			log.Fatal(err)
		}
		defer postgres.Close()
		store = postgres
		log.Print("using PostgreSQL repository")
	}

	output := runtime.NewOutputBuffer()
	forwarder := runtime.NewVectorForwarder(*vectorSocket, 1024)
	if forwarder != nil {
		output.SetForwarder(forwarder)
		defer forwarder.Close()
	}
	durationTelemetry, err := runtime.NewPersistentDurationTelemetry(filepath.Join(*configDir, "duration-telemetry.json"))
	if err != nil {
		log.Fatal(err)
	}
	var publisher runtimePublisher
	var runtimeEnsurer api.RuntimeEnsurer
	var remoteRunner *runner.Client
	var closeRuntime func()
	traefikStore := config.NewTraefikStore(*authURL, *workerHost)
	if *oidcPublic != "" {
		if parsed, err := url.Parse(*oidcPublic); err == nil {
			traefikStore.SetAuthHost(parsed.Hostname())
		} else {
			log.Fatal(err)
		}
	}
	var traefikWriter config.TraefikWriter = traefikStore
	if *traefikFile != "" {
		traefikWriter = config.NewWriter("", *traefikFile, *authURL, *workerHost)
		if parsed, err := url.Parse(*oidcPublic); *oidcPublic != "" {
			if err != nil {
				log.Fatal(err)
			}
			traefikWriter.(*config.Writer).SetAuthHost(parsed.Hostname())
		}
	}
	if *runnerURL != "" && *runnerToken == "" {
		log.Fatal("runner token is required in remote runner mode")
	}
	if *runnerToken == "" {
		*runnerToken = rand.Text()
	}
	if *runnerURL != "" {
		client := runner.NewClient(*runnerURL, *runnerToken, traefikWriter)
		remoteRunner = client
		publisher = client
		runtimeEnsurer = client
	} else {
		writer := config.NewRuntimeWriter(filepath.Join(*configDir, "workerd.capnp"), traefikWriter)
		writer.SetNanoflareRuntimeAddr(*runtimeAddr)
		writer.SetNetworkAllow(config.ParseNetworkAllow(*workerdNetworkAllow))
		writer.SetWFPSockets(wfpSockets)
		dnsConfig, err := dnsresolver.LoadConfig(*dnsConfigValue)
		if err != nil {
			log.Fatal(err)
		}
		dnsServer, err := dnsresolver.New(dnsConfig, *dnsAddr)
		if err != nil {
			log.Fatal(err)
		}
		if err := dnsServer.Start(); err != nil {
			log.Fatal(err)
		}
		writer.SetWorkerdDNSAddr(dnsServer.Addr())
		writer.SetDNSProfiles(dnsConfig.ProfileNames())
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = dnsServer.Close(ctx)
		}()
		if strings.TrimSpace(*egressProxyURL) != "" {
			adapter, err := egress.New(egress.Config{ProxyResolves: *egressProxyResolves, NetworkAllow: config.ParseNetworkAllow(*workerdNetworkAllow), ProxyURL: *egressProxyURL, CAFiles: egress.ParseCAFiles(*egressCAFiles), NoProxy: egress.ParseNoProxy(*egressNoProxy), Addr: *egressAddr})
			if err != nil {
				log.Fatal(err)
			}
			if err := adapter.Start(); err != nil {
				log.Fatal(err)
			}
			writer.SetWorkerdEgressAddr(adapter.Addr())
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = adapter.Close(ctx)
			}()
		}
		manager := runtime.NewDynamicManager(
			writer,
			runtime.CommandLauncher{Executable: *workerd, Output: output},
			*configDir,
			filepath.Join(*configDir, "workerd.capnp"),
			*socketDir,
			10*time.Second,
			5*time.Second,
		)
		manager.SetRuntimeControlToken(*runnerToken)
		publisher = manager
		runtimeEnsurer = manager
		closeRuntime = func() {
			if err := manager.Close(); err != nil {
				log.Printf("close runtime manager: %v", err)
			}
		}
	}
	if closeRuntime != nil {
		defer closeRuntime()
	}

	var outputReader nanoflare.WorkerOutputReader = output
	if strings.TrimSpace(*lokiURL) != "" {
		outputReader = logs.NewLokiReader(*lokiURL)
		log.Printf("using Loki worker output at %s", *lokiURL)
	}
	service := nanoflare.NewServiceWithConsole(store, publisher, objectStore, outputReader, metrics.NewCombinedReader(metrics.NewClient(*prometheus), durationTelemetry))
	compatibilityDate, err := runtime.CompatibilityDate(*workerd)
	if err != nil {
		log.Fatal(err)
	}
	if err := service.SetMaxCompatibilityDate(compatibilityDate); err != nil {
		log.Fatal(err)
	}
	log.Printf("workerd compatibility date: %s", compatibilityDate)
	litestream := database.NewLitestreamSupervisor(*litestreamEnabled, *litestreamBin, *litestreamConfig)
	if litestream.Enabled() && strings.TrimSpace(*litestreamConfig) == "" {
		replica, err := generatedLitestreamReplicaConfig()
		if err != nil {
			log.Fatal(err)
		}
		if err := litestream.UseGeneratedConfig(filepath.Join(*configDir, "litestream.generated.yml"), replica); err != nil {
			log.Fatal(err)
		}
	}
	dbRuntime, err := database.NewSQLiteManager(*dbDir, litestream)
	if err != nil {
		log.Fatal(err)
	}
	service.SetDBExecutor(dbRuntime)
	if litestream.Enabled() {
		if err := litestream.Start(context.Background()); err != nil {
			log.Fatal(err)
		}
		log.Print("using Litestream for SQLite backup replication")
	}
	service.SetPlatformDispatchEnabled(*platformDispatchEnabled)
	var redactions []string
	if err := json.Unmarshal([]byte(*workerLogPatterns), &redactions); err != nil {
		log.Fatal(err)
	}
	if err := service.SetTelemetryPolicy(nanoflare.TelemetryPolicy{Enabled: *workerLogsEnabled, SamplePercent: *workerLogSample, RedactPatterns: redactions}); err != nil {
		log.Fatal(err)
	}
	if *baseHostname != "" {
		if err := service.SetBaseHostname(*baseHostname); err != nil {
			log.Fatal(err)
		}
	}
	if value := os.Getenv("NANOFLARE_SECRET_KEY"); value != "" {
		codec, err := nanoflare.NewSecretCodec(value)
		if err != nil {
			log.Fatal(err)
		}
		service.SetSecretCodec(codec)
	}
	if checker, ok := publisher.(interface {
		RuntimeDeploymentLive(context.Context, string, string) (bool, error)
	}); ok {
		service.SetRuntimeDeploymentChecker(checker.RuntimeDeploymentLive)
	}
	if checker, ok := publisher.(interface {
		RuntimeInstanceLive(context.Context, string, string, string) (bool, error)
	}); ok {
		service.SetRuntimeInstanceChecker(checker.RuntimeInstanceLive)
	}

	var authenticator api.Authenticator
	if *oidcIssuer != "" && *oidcAudience != "" {
		clientID := *oidcClientID
		if clientID == "" {
			clientID = *oidcAudience
		}
		verifier := oidc.NewBrowserVerifier(*oidcIssuer, *oidcAudience, *oidcEmail, clientID, *oidcSecret, *oidcPublic, *oidcCookie, nil)
		if err := verifier.ValidateBrowserConfig(); err != nil {
			log.Fatal(err)
		}
		if verifier.BrowserFlowEnabled() {
			log.Printf("nanoflared oidc callback URL %s", verifier.RedirectURL())
		}
		authenticator = verifier
	}
	controlAuth := nanoflare.NewControlAuthService(store, *authSecret)
	oauth := nanoflare.NewOAuthService(store)
	server := api.NewServerWithRuntimeAndOAuth(service, traefikStore, *traefikToken, authenticator, controlAuth, oauth, runtimeEnsurer)
	server.SetDurationTelemetry(durationTelemetry)
	server.SetMetricsToken(*metricsToken)
	server.SetCloudflareCompatibilityEnabled(*cloudflareCompatibilityEnabled)
	server.SetLogForwarderStats(forwarder)
	if remoteRunner != nil {
		server.SetRunnerResourceReader(remoteRunner.Resources)
	}
	server.SetPartnerService(nanoflare.NewPartnerService(store))
	if *controlOIDCIssuer != "" && *controlOIDCClientID != "" && *controlOIDCPublic != "" {
		controlVerifier := oidc.NewConsoleVerifier(*controlOIDCIssuer, *controlOIDCEmail, *controlOIDCClientID, *controlOIDCSecret, *controlOIDCPublic, nil)
		if err := controlVerifier.ValidateBrowserConfig(); err != nil {
			log.Fatal(err)
		}
		log.Printf("nanoflared control oidc callback URL %s", controlVerifier.RedirectURL())
		server.SetControlOIDC(controlVerifier)
		server.SetControlOIDCDirectLogin(*controlOIDCDirect)
	}
	dispatchTransport, err := egress.NewHTTPTransport(egress.Config{ProxyResolves: *egressProxyResolves, NetworkAllow: config.ParseNetworkAllow(*workerdNetworkAllow), ProxyURL: *egressProxyURL, CAFiles: egress.ParseCAFiles(*egressCAFiles), NoProxy: egress.ParseNoProxy(*egressNoProxy)})
	if err != nil {
		log.Fatal(err)
	}
	runtimeMux := newRuntimeMuxWithControl(service, server, durationTelemetry, dispatchTransport, *runnerToken, output)
	defer runtimeMux.Close()

	runtimeServer := &http.Server{Addr: *runtimeAddr, Handler: runtimeMux}
	privateListener, err := net.Listen("tcp", *runtimeAddr)
	if err != nil {
		log.Fatal(err)
	}
	defer privateListener.Close()
	go func() {
		log.Printf("nanoflared runtime API listening on %s", *runtimeAddr)
		if err := runtimeServer.Serve(privateListener); err != nil && err != http.ErrServerClosed {
			log.Fatalf("runtime API: %v", err)
		}
	}()

	if err := service.ReconcileRuntime(); err != nil {
		log.Fatal(err)
	}

	log.Printf("nanoflared listening on %s", *addr)
	log.Printf("runtime configs will be written to %s", *configDir)
	httpServer := &http.Server{Addr: *addr, Handler: server}
	shutdown, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	stopAssetCleanup := service.StartAssetUploadCleanup(shutdown, time.Minute, func(err error) { log.Printf("asset staging cleanup: %v", err) })
	defer stopAssetCleanup()
	if remoteRunner != nil {
		stopReconciliation := remoteRunner.StartReconciliation(shutdown, 5*time.Second, service.ReconcileRuntime, func(err error) {
			log.Printf("remote runner reconciliation: %v", err)
		})
		defer stopReconciliation()
	}
	go func() {
		<-shutdown.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(ctx)
		runtimeServer.Shutdown(ctx)
	}()
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envBoolOrDefault(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		log.Fatal(err)
	}
	return parsed
}

func envFloatOrDefault(name string, fallback float64) float64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		log.Fatal(err)
	}
	return parsed
}

func newRuntimeMux(service *nanoflare.Service, server *api.Server, durationTelemetry *runtime.DurationTelemetry, outputs ...api.RuntimeLogAppender) *privateRuntimeMux {
	return newRuntimeMuxWithControl(service, server, durationTelemetry, nil, "", outputs...)
}
func newRuntimeMuxWithOutbound(service *nanoflare.Service, server *api.Server, durationTelemetry *runtime.DurationTelemetry, outbound http.RoundTripper, outputs ...api.RuntimeLogAppender) *privateRuntimeMux {
	return newRuntimeMuxWithControl(service, server, durationTelemetry, outbound, "", outputs...)
}
func newRuntimeMuxWithControl(service *nanoflare.Service, server *api.Server, durationTelemetry *runtime.DurationTelemetry, outbound http.RoundTripper, token string, outputs ...api.RuntimeLogAppender) *privateRuntimeMux {
	runtimeMux := http.NewServeMux()
	executions := api.NewRuntimeExecutionServer(service, token)
	runtimeMux.Handle("/internal/runtime/executions/", executions)
	if durationTelemetry != nil {
		service.SetDispatchObservationSink(func(event nanoflare.DispatchObservation) {
			durationTelemetry.RecordBatch([]runtime.DurationTraceEvent{{ScriptName: event.WorkerID, EventTimestamp: float64(event.At.UnixMilli()), DurationMs: event.ElapsedMS, Outcome: event.Outcome}})
		})
	}

	runtimeKV := api.NewRuntimeKVServer(service)
	runtimeDB := api.NewRuntimeDBServer(service)
	runtimeAssets := api.NewRuntimeAssetServer(service)
	runtimeDurations := api.NewRuntimeDurationServer(service, durationTelemetry)
	var output api.RuntimeLogAppender
	if len(outputs) > 0 {
		output = outputs[0]
	}
	runtimeMux.Handle("/internal/runtime/dispatch/", api.NewRuntimeDispatchServer(service, server, outbound).SetLogOutput(output))
	runtimeMux.Handle("/internal/runtime/access", server.RuntimeAccessHandler())
	runtimeMux.Handle("/internal/runtime/logs", api.NewRuntimeLogServer(service, output))
	runtimeMux.Handle("/internal/runtime/objects/", server)
	runtimeMux.Handle("/internal/runtime/durations", runtimeDurations)
	resourceHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Nanoflare-Object-Bucket-ID") != "" {
			server.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-Nanoflare-Binding") == "assets" {
			runtimeAssets.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-Nanoflare-Binding") == "db" {
			runtimeDB.ServeHTTP(w, r)
			return
		}
		runtimeKV.ServeHTTP(w, r)
	})
	runtimeMux.Handle("/", resourceHandler)
	runtimeMux.Handle("/internal/runtime/resources/", http.StripPrefix("/internal/runtime/resources", resourceHandler))
	runtimeMux.Handle("/internal/runtime/assets/", runtimeAssets)
	return &privateRuntimeMux{ServeMux: runtimeMux, executions: executions}
}

// privateRuntimeMux owns control sessions for the lifetime of the private server.
type privateRuntimeMux struct {
	*http.ServeMux
	executions *api.RuntimeExecutionServer
}

func (m *privateRuntimeMux) Close() error { return m.executions.Close() }
