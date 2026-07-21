package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/runner"
	"github.com/clas/nanoflare/internal/runtime"
)

func main() {
	var (
		addr                 = flag.String("addr", "127.0.0.1:8090", "runner control API listen address")
		configDir            = flag.String("config-dir", "./var/runner", "directory for generated runtime configuration")
		workerd              = flag.String("workerd", "workerd", "path to the workerd executable")
		workerdNetworkAllow  = flag.String("workerd-network-allow", envOrDefault("NANOFLARE_WORKERD_NETWORK_ALLOW", strings.Join(config.DefaultNetworkAllow(), ",")), "comma-separated workerd outbound network allow list")
		portHost             = flag.String("runtime-port-host", "127.0.0.1", "host used to allocate and health-check workerd sockets")
		portStart            = flag.Int("runtime-port-start", 10000, "first port considered for workerd pool generations")
		nanoflareRuntimeAddr = flag.String("nanoflare-runtime-addr", "127.0.0.1:8081", "nanoflared private runtime KV API address reachable from workerd")
		token                = flag.String("token", os.Getenv("NANOFLARE_RUNNER_TOKEN"), "nanoflared authentication token")
	)
	flag.Parse()

	if *token == "" {
		log.Fatal("runner token is required")
	}
	if err := os.MkdirAll(*configDir, 0o700); err != nil {
		log.Fatal(err)
	}
	if err := os.Chmod(*configDir, 0o700); err != nil {
		log.Fatal(err)
	}

	output := runtime.NewOutputBuffer()
	writer := config.NewWriter(
		filepath.Join(*configDir, "workerd.capnp"),
		filepath.Join(*configDir, "unused-traefik.yml"),
		"",
		"",
	)
	writer.SetNanoflareRuntimeAddr(*nanoflareRuntimeAddr)
	writer.SetNetworkAllow(config.ParseNetworkAllow(*workerdNetworkAllow))
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
	manager.SetRetireDelay(2 * time.Second)
	manager.SetAutoRestart(false)
	defer manager.Close()

	server := &http.Server{Addr: *addr, Handler: runner.NewServer(manager, *token)}
	shutdown, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	go func() {
		<-shutdown.Done()
		server.Close()
	}()
	log.Printf("nanoflare-runner listening on %s", *addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
