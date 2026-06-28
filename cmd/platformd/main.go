package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/clas/platform/internal/api"
	"github.com/clas/platform/internal/config"
	"github.com/clas/platform/internal/database"
	"github.com/clas/platform/internal/metrics"
	"github.com/clas/platform/internal/objects"
	"github.com/clas/platform/internal/platform"
	"github.com/clas/platform/internal/runner"
	"github.com/clas/platform/internal/runtime"
)

type runtimePublisher interface {
	Write([]platform.ActiveDeployment) error
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
		workerd      = flag.String("workerd", "workerd", "path to the workerd executable")
		portHost     = flag.String("runtime-port-host", "127.0.0.1", "host used to allocate and health-check workerd sockets")
		portStart    = flag.Int("runtime-port-start", 10000, "first port considered for workerd pool generations")
		prometheus   = flag.String("prometheus-url", "http://127.0.0.1:9090", "Prometheus base URL for worker traffic metrics")
		runnerURL    = flag.String("runner-url", "", "platform-runner control API URL; empty starts workerd directly")
		runnerToken  = flag.String("runner-token", os.Getenv("PLATFORM_RUNNER_TOKEN"), "platform-runner authentication token")
		traefikToken = flag.String("traefik-token", os.Getenv("PLATFORM_TRAEFIK_TOKEN"), "Traefik HTTP provider authentication token")
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

	var store platform.Repository = platform.NewStore()
	var objectStore platform.ObjectStore
	if endpoint := os.Getenv("MINIO_ENDPOINT"); endpoint != "" {
		secure, err := strconv.ParseBool(envOrDefault("MINIO_SECURE", "false"))
		if err != nil {
			log.Fatal(err)
		}
		objectStore, err = objects.Open(ctx, objects.MinIOConfig{
			Endpoint:  endpoint,
			AccessKey: os.Getenv("MINIO_ACCESS_KEY"),
			SecretKey: os.Getenv("MINIO_SECRET_KEY"),
			Bucket:    envOrDefault("MINIO_BUCKET", "platform"),
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
	var publisher runtimePublisher
	var closeRuntime func()
	traefikStore := config.NewTraefikStore(*authURL, *workerHost)
	var traefikWriter config.TraefikWriter = traefikStore
	if *traefikFile != "" {
		traefikWriter = config.NewWriter("", *traefikFile, *authURL, *workerHost)
	}
	if *runnerURL != "" {
		if *runnerToken == "" {
			log.Fatal("runner token is required when runner URL is configured")
		}
		publisher = runner.NewClient(*runnerURL, *runnerToken, traefikWriter)
		log.Printf("using platform-runner at %s", *runnerURL)
	} else {
		writer := config.NewRuntimeWriter(filepath.Join(*configDir, "workerd.capnp"), traefikWriter)
		writer.SetPlatformRuntimeAddr(*runtimeAddr)
		manager := runtime.NewManager(
			writer,
			runtime.CommandLauncher{Executable: *workerd, Output: output},
			*configDir,
			filepath.Join(*configDir, "workerd.capnp"),
			*portHost,
			*portStart,
			10*time.Second,
			5*time.Second,
		)
		publisher = manager
		closeRuntime = func() {
			if err := manager.Close(); err != nil {
				log.Printf("close runtime manager: %v", err)
			}
		}
	}
	if closeRuntime != nil {
		defer closeRuntime()
	}

	service := platform.NewServiceWithConsole(store, publisher, objectStore, output, metrics.NewClient(*prometheus))
	active, err := service.ActiveDeployments()
	if err != nil {
		log.Fatal(err)
	}
	if err := publisher.Write(active); err != nil {
		log.Fatal(err)
	}
	server := api.NewServerWithTraefik(service, traefikStore, *traefikToken)
	runtimeServer := &http.Server{Addr: *runtimeAddr, Handler: api.NewRuntimeKVServer(service)}
	go func() {
		log.Printf("platformd runtime KV API listening on %s", *runtimeAddr)
		if err := runtimeServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("runtime KV API: %v", err)
		}
	}()

	log.Printf("platformd listening on %s", *addr)
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
