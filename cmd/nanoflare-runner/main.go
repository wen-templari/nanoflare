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
	"github.com/clas/nanoflare/internal/egress"
	"github.com/clas/nanoflare/internal/runner"
	"github.com/clas/nanoflare/internal/runtime"
)

func main() {
	var (
		addr                 = flag.String("addr", "127.0.0.1:8090", "runner control API listen address")
		configDir            = flag.String("config-dir", "./var/runner", "directory for generated runtime configuration")
		workerd              = flag.String("workerd", "workerd", "path to the workerd executable")
		workerdNetworkAllow  = flag.String("workerd-network-allow", envOrDefault("NANOFLARE_WORKERD_NETWORK_ALLOW", strings.Join(config.DefaultNetworkAllow(), ",")), "comma-separated workerd outbound network allow list")
		egressProxyURL       = flag.String("workerd-egress-proxy-url", os.Getenv("NANOFLARE_WORKERD_EGRESS_PROXY_URL"), "explicit HTTP proxy URL for Worker outbound traffic")
		egressCAFiles        = flag.String("workerd-egress-ca-files", os.Getenv("NANOFLARE_WORKERD_EGRESS_CA_FILES"), "comma-separated corporate CA PEM files")
		egressAddr           = flag.String("workerd-egress-addr", envOrDefault("NANOFLARE_WORKERD_EGRESS_ADDR", "127.0.0.1:8082"), "private Worker egress adapter address")
		portHost             = flag.String("runtime-port-host", "127.0.0.1", "host used to allocate and health-check workerd sockets")
		portStart            = flag.Int("runtime-port-start", 10000, "first port considered for workerd pool generations")
		nanoflareRuntimeAddr = flag.String("nanoflare-runtime-addr", "127.0.0.1:8081", "nanoflared private runtime KV API address reachable from workerd")
		token                = flag.String("token", os.Getenv("NANOFLARE_RUNNER_TOKEN"), "nanoflared authentication token")
		vectorSocket         = flag.String("log-vector-socket", os.Getenv("NANOFLARE_LOG_VECTOR_SOCKET"), "optional local Vector Unix socket for structured runtime output")
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
	forwarder := runtime.NewVectorForwarder(*vectorSocket, 1024)
	if forwarder != nil {
		output.SetForwarder(forwarder)
		defer forwarder.Close()
	}
	writer := config.NewWriter(
		filepath.Join(*configDir, "workerd.capnp"),
		filepath.Join(*configDir, "unused-traefik.yml"),
		"",
		"",
	)
	writer.SetNanoflareRuntimeAddr(*nanoflareRuntimeAddr)
	writer.SetNetworkAllow(config.ParseNetworkAllow(*workerdNetworkAllow))
	if strings.TrimSpace(*egressProxyURL) != "" {
		adapter, err := egress.New(egress.Config{ProxyURL: *egressProxyURL, CAFiles: egress.ParseCAFiles(*egressCAFiles), Addr: *egressAddr})
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
