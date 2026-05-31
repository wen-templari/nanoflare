package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/clas/platform/internal/platform"
)

type Writer struct {
	workerdPath string
	traefikPath string
	authURL     string
}

func NewWriter(workerdPath, traefikPath, authURL string) *Writer {
	return &Writer{workerdPath: workerdPath, traefikPath: traefikPath, authURL: authURL}
}

func (w *Writer) Write(active []platform.ActiveDeployment) error {
	if err := writeAtomic(w.workerdPath, []byte(Workerd(relativeBundles(filepath.Dir(w.workerdPath), active)))); err != nil {
		return err
	}
	return writeAtomic(w.traefikPath, []byte(Traefik(active, w.authURL)))
}

func Workerd(active []platform.ActiveDeployment) string {
	var out strings.Builder
	out.WriteString("using Workerd = import \"/workerd/workerd.capnp\";\n\n")
	out.WriteString("const config :Workerd.Config = (\n  services = [\n")
	for _, item := range active {
		fmt.Fprintf(&out, "    (name = %s, worker = .%s),\n", quote(item.App.ID), workerName(item.App.ID))
	}
	out.WriteString("  ],\n\n  sockets = [\n")
	for _, item := range active {
		fmt.Fprintf(&out, "    (name = %s, address = %s, http = (), service = %s),\n",
			quote(item.App.ID), quote(fmt.Sprintf("*:%d", item.Deployment.Port)), quote(item.App.ID))
	}
	out.WriteString("  ]\n);\n")
	for _, item := range active {
		fmt.Fprintf(&out, "\nconst %s :Workerd.Worker = (\n", workerName(item.App.ID))
		fmt.Fprintf(&out, "  serviceWorkerScript = embed %s,\n", quote(item.Deployment.BundlePath))
		fmt.Fprintf(&out, "  compatibilityDate = %s,\n", quote(item.Deployment.CompatibilityDate))
		out.WriteString(");\n")
	}
	return out.String()
}

func Traefik(active []platform.ActiveDeployment, authURL string) string {
	var out strings.Builder
	out.WriteString("http:\n  middlewares:\n    platform-auth:\n      forwardAuth:\n")
	fmt.Fprintf(&out, "        address: %s\n        authResponseHeaders:\n          - X-Platform-Context\n", yamlQuote(authURL))
	out.WriteString("  routers:\n")
	for _, item := range active {
		name := identifier(item.App.ID)
		fmt.Fprintf(&out, "    %s:\n      rule: %s\n      entryPoints:\n        - websecure\n      middlewares:\n        - platform-auth\n      service: %s\n      tls: {}\n",
			name, yamlQuote("Host(`"+item.App.Hostname+"`)"), name)
	}
	out.WriteString("  services:\n")
	for _, item := range active {
		name := identifier(item.App.ID)
		fmt.Fprintf(&out, "    %s:\n      loadBalancer:\n        servers:\n          - url: %s\n",
			name, yamlQuote(fmt.Sprintf("http://127.0.0.1:%d/", item.Deployment.Port)))
	}
	return out.String()
}

func writeAtomic(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".platformd-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func identifier(value string) string {
	return strings.ReplaceAll(value, "-", "_")
}

func workerName(value string) string {
	var out strings.Builder
	out.WriteString("worker")
	upperNext := true
	for _, char := range value {
		if char == '-' {
			upperNext = true
			continue
		}
		if upperNext && char >= 'a' && char <= 'z' {
			char -= 'a' - 'A'
		}
		upperNext = false
		out.WriteRune(char)
	}
	return out.String()
}

func relativeBundles(configDir string, active []platform.ActiveDeployment) []platform.ActiveDeployment {
	result := make([]platform.ActiveDeployment, len(active))
	copy(result, active)
	for i := range result {
		if !filepath.IsAbs(result[i].Deployment.BundlePath) {
			continue
		}
		relative, err := filepath.Rel(configDir, result[i].Deployment.BundlePath)
		if err == nil {
			result[i].Deployment.BundlePath = relative
		}
	}
	return result
}

func quote(value string) string {
	return strconv.Quote(value)
}

func yamlQuote(value string) string {
	return strconv.Quote(value)
}
