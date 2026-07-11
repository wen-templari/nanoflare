package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/database"
	"github.com/clas/nanoflare/internal/metrics"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/objects"
	"github.com/clas/nanoflare/internal/oidc"
	"github.com/clas/nanoflare/internal/runtime"
)

type runtimePublisher interface {
	Write([]nanoflare.ActiveDeployment) error
}

func main() {
	if err := loadEnvFile(".env"); err != nil {
		log.Fatal(err)
	}

	var (
		addr         = flag.String("addr", ":8080", "HTTP listen address")
		runtimeAddr  = flag.String("runtime-addr", "127.0.0.1:8081", "private runtime KV API listen address")
		configDir    = flag.String("config-dir", "./var/generated", "directory for generated runtime configuration")
		traefikFile  = flag.String("traefik-file", "", "optional Traefik dynamic configuration file fallback")
		authURL      = flag.String("auth-url", "http://host.docker.internal:8080/internal/auth/verify", "Traefik ForwardAuth callback URL")
		workerHost   = flag.String("worker-host", "host.docker.internal", "hostname Traefik uses to reach workerd sockets")
		baseHostname = flag.String("base-hostname", os.Getenv("NANOFLARE_BASE_HOSTNAME"), "base DNS hostname used when worker hostnames are omitted")
		workerd      = flag.String("workerd", "workerd", "path to the workerd executable")
		portHost     = flag.String("runtime-port-host", "127.0.0.1", "host used to allocate and health-check workerd sockets")
		portStart    = flag.Int("runtime-port-start", 10000, "first port considered for workerd pool generations")
		idleTimeout  = flag.Duration("runtime-idle-timeout", 30*time.Second, "idle duration before a lazy worker runtime is stopped")
		prometheus   = flag.String("prometheus-url", "http://127.0.0.1:9090", "Prometheus base URL for worker traffic metrics")
		runnerURL    = flag.String("runner-url", "", "nanoflare-runner control API URL; empty starts workerd directly")
		runnerToken  = flag.String("runner-token", os.Getenv("NANOFLARE_RUNNER_TOKEN"), "nanoflare-runner authentication token")
		traefikToken = flag.String("traefik-token", os.Getenv("NANOFLARE_TRAEFIK_TOKEN"), "Traefik HTTP provider authentication token")
		authSecret   = flag.String("control-auth-secret", envOrDefault("NANOFLARE_AUTH_SECRET", "nanoflare-development-control-secret"), "JWT signing secret for control-plane email/password auth")
		oidcIssuer   = flag.String("oidc-issuer", os.Getenv("NANOFLARE_OIDC_ISSUER"), "OIDC issuer URL for protected worker routes")
		oidcAudience = flag.String("oidc-audience", os.Getenv("NANOFLARE_OIDC_AUDIENCE"), "OIDC audience expected in protected worker JWTs")
		oidcEmail    = flag.String("oidc-email-claim", envOrDefault("NANOFLARE_OIDC_EMAIL_CLAIM", "email"), "OIDC email claim fallback used when userinfo omits email")
		oidcClientID = flag.String("oidc-client-id", os.Getenv("NANOFLARE_OIDC_CLIENT_ID"), "OIDC client ID for browser login flow; defaults to oidc-audience when omitted")
		oidcSecret   = flag.String("oidc-client-secret", os.Getenv("NANOFLARE_OIDC_CLIENT_SECRET"), "OIDC client secret for browser login flow")
		oidcPublic   = flag.String("oidc-public-url", os.Getenv("NANOFLARE_OIDC_PUBLIC_URL"), "Public control-plane base URL for browser login callback routing, for example https://nanoflare.example.com:8443")
		oidcCookie   = flag.String("oidc-cookie-domain", os.Getenv("NANOFLARE_OIDC_COOKIE_DOMAIN"), "Optional parent cookie domain shared by the callback host and worker hosts, for example .local.nbtca.space")
	)
	flag.Parse()

	if *traefikFile == "" && *traefikToken == "" {
		log.Fatal("Traefik token is required when HTTP discovery is enabled")
	}
	if err := os.MkdirAll(*configDir, 0o700); err != nil {
		log.Fatal(err)
	}
	if err := os.Chmod(*configDir, 0o700); err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

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
	durationTelemetry := runtime.NewDurationTelemetry()
	var publisher runtimePublisher
	var runtimeEnsurer api.RuntimeEnsurer
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
	if *runnerURL != "" {
		_ = *runnerToken
		log.Fatal("lazy runtime startup is not implemented for nanoflare-runner; run without -runner-url for lazy local workers")
	}
	writer := config.NewRuntimeWriter(filepath.Join(*configDir, "workerd.capnp"), traefikWriter)
	writer.SetNanoflareRuntimeAddr(*runtimeAddr)
	manager := runtime.NewLazyManager(
		writer,
		runtime.CommandLauncher{Executable: *workerd, Output: output},
		*configDir,
		*portHost,
		*portStart,
		10*time.Second,
		5*time.Second,
		*idleTimeout,
	)
	publisher = manager
	runtimeEnsurer = manager
	closeRuntime = func() {
		if err := manager.Close(); err != nil {
			log.Printf("close runtime manager: %v", err)
		}
	}
	if closeRuntime != nil {
		defer closeRuntime()
	}

	service := nanoflare.NewServiceWithConsole(store, publisher, objectStore, output, metrics.NewCombinedReader(metrics.NewClient(*prometheus), durationTelemetry))
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
	active, err := service.ActiveDeployments()
	if err != nil {
		log.Fatal(err)
	}
	if err := publisher.Write(active); err != nil {
		log.Fatal(err)
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
	server := api.NewServerWithRuntime(service, traefikStore, *traefikToken, authenticator, controlAuth, runtimeEnsurer)
	runtimeMux := newRuntimeMux(service, server, durationTelemetry)
	runtimeServer := &http.Server{Addr: *runtimeAddr, Handler: runtimeMux}
	go func() {
		log.Printf("nanoflared runtime API listening on %s", *runtimeAddr)
		if err := runtimeServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("runtime API: %v", err)
		}
	}()

	log.Printf("nanoflared listening on %s", *addr)
	log.Printf("runtime configs will be written to %s", *configDir)
	httpServer := &http.Server{Addr: *addr, Handler: server}
	shutdown, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
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

func newRuntimeMux(service *nanoflare.Service, server *api.Server, durationTelemetry *runtime.DurationTelemetry) *http.ServeMux {
	runtimeMux := http.NewServeMux()
	runtimeKV := api.NewRuntimeKVServer(service)
	runtimeAssets := api.NewRuntimeAssetServer(service)
	runtimeDurations := api.NewRuntimeDurationServer(durationTelemetry)
	runtimeMux.Handle("/internal/runtime/objects/", server)
	runtimeMux.Handle("/internal/runtime/durations", runtimeDurations)
	runtimeMux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Nanoflare-Binding") == "assets" {
			runtimeAssets.ServeHTTP(w, r)
			return
		}
		runtimeKV.ServeHTTP(w, r)
	}))
	runtimeMux.Handle("/internal/runtime/assets/", runtimeAssets)
	return runtimeMux
}
