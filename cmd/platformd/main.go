package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/clas/platform/internal/api"
	"github.com/clas/platform/internal/config"
	"github.com/clas/platform/internal/platform"
)

func main() {
	var (
		addr      = flag.String("addr", ":8080", "HTTP listen address")
		configDir = flag.String("config-dir", "./var/generated", "directory for generated runtime configuration")
		authURL   = flag.String("auth-url", "http://127.0.0.1:8080/internal/auth/verify", "Traefik ForwardAuth callback URL")
	)
	flag.Parse()

	if err := os.MkdirAll(*configDir, 0o755); err != nil {
		log.Fatal(err)
	}

	store := platform.NewStore()
	writer := config.NewWriter(
		filepath.Join(*configDir, "workerd.capnp"),
		filepath.Join(*configDir, "traefik.yml"),
		*authURL,
	)
	service := platform.NewService(store, writer)
	server := api.NewServer(service)

	log.Printf("platformd listening on %s", *addr)
	log.Printf("generated configs will be written to %s", *configDir)
	if err := http.ListenAndServe(*addr, server); err != nil {
		log.Fatal(err)
	}
}
