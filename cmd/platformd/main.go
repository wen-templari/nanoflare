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
	"github.com/clas/platform/internal/objects"
	"github.com/clas/platform/internal/platform"
	"github.com/clas/platform/internal/runtime"
)

func main() {
	var (
		addr       = flag.String("addr", ":8080", "HTTP listen address")
		configDir  = flag.String("config-dir", "./var/generated", "directory for generated runtime configuration")
		authURL    = flag.String("auth-url", "http://127.0.0.1:8080/internal/auth/verify", "Traefik ForwardAuth callback URL")
		workerHost = flag.String("worker-host", "127.0.0.1", "hostname Traefik uses to reach workerd sockets")
		workerd    = flag.String("workerd", "workerd", "path to the workerd executable")
		portHost   = flag.String("runtime-port-host", "127.0.0.1", "host used to allocate and health-check workerd sockets")
		portStart  = flag.Int("runtime-port-start", 10000, "first port considered for workerd pool generations")
	)
	flag.Parse()

	if err := os.MkdirAll(*configDir, 0o755); err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var store platform.Repository = platform.NewStore()
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		postgres, err := database.Open(ctx, databaseURL)
		if err != nil {
			log.Fatal(err)
		}
		defer postgres.Close()
		store = postgres
		log.Print("using PostgreSQL repository")
	}

	writer := config.NewWriter(
		filepath.Join(*configDir, "workerd.capnp"),
		filepath.Join(*configDir, "traefik.yml"),
		*authURL,
		*workerHost,
	)
	manager := runtime.NewManager(
		writer,
		runtime.CommandLauncher{Executable: *workerd},
		*configDir,
		filepath.Join(*configDir, "workerd.capnp"),
		*portHost,
		*portStart,
		10*time.Second,
		5*time.Second,
	)
	defer manager.Close()
	active, err := store.ActiveDeployments()
	if err != nil {
		log.Fatal(err)
	}
	if err := manager.Write(active); err != nil {
		log.Fatal(err)
	}

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

	service := platform.NewServiceWithObjects(store, manager, objectStore)
	server := api.NewServer(service)

	log.Printf("platformd listening on %s", *addr)
	log.Printf("generated configs will be written to %s", *configDir)
	httpServer := &http.Server{Addr: *addr, Handler: server}
	shutdown, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	go func() {
		<-shutdown.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(ctx)
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
