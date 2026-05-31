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
	workerHost  string
	runtimeAddr string
}

type TraefikWriter interface {
	WriteTraefik([]platform.ActiveDeployment) error
}

type RuntimeWriter struct {
	workerdPath string
	traefik     TraefikWriter
	runtimeAddr string
}

func NewWriter(workerdPath, traefikPath, authURL, workerHost string) *Writer {
	return &Writer{workerdPath: workerdPath, traefikPath: traefikPath, authURL: authURL, workerHost: workerHost, runtimeAddr: "127.0.0.1:8081"}
}

func NewRuntimeWriter(workerdPath string, traefik TraefikWriter) *RuntimeWriter {
	return &RuntimeWriter{workerdPath: workerdPath, traefik: traefik, runtimeAddr: "127.0.0.1:8081"}
}

func (w *Writer) SetPlatformRuntimeAddr(addr string) {
	w.runtimeAddr = addr
}

func (w *RuntimeWriter) SetPlatformRuntimeAddr(addr string) {
	w.runtimeAddr = addr
}

func (w *Writer) Write(active []platform.ActiveDeployment) error {
	if err := w.WriteWorkerd(w.workerdPath, active); err != nil {
		return err
	}
	return w.WriteTraefik(active)
}

func (w *Writer) WriteWorkerd(path string, active []platform.ActiveDeployment) error {
	return writeAtomic(path, []byte(WorkerdWithRuntimeAddr(active, w.runtimeAddr)))
}

func (w *Writer) WriteTraefik(active []platform.ActiveDeployment) error {
	return writeAtomic(w.traefikPath, []byte(Traefik(active, w.authURL, w.workerHost)))
}

func (w *RuntimeWriter) WriteWorkerd(path string, active []platform.ActiveDeployment) error {
	return writeAtomic(path, []byte(WorkerdWithRuntimeAddr(active, w.runtimeAddr)))
}

func (w *RuntimeWriter) WriteTraefik(active []platform.ActiveDeployment) error {
	return w.traefik.WriteTraefik(active)
}

func Workerd(active []platform.ActiveDeployment) string {
	return WorkerdWithRuntimeAddr(active, "127.0.0.1:8081")
}

func WorkerdWithRuntimeAddr(active []platform.ActiveDeployment, runtimeAddr string) string {
	var out strings.Builder
	out.WriteString("using Workerd = import \"/workerd/workerd.capnp\";\n\n")
	out.WriteString("const config :Workerd.Config = (\n  services = [\n")
	for _, item := range active {
		fmt.Fprintf(&out, "    (name = %s, worker = .%s),\n", quote(item.App.ID), workerName(item.App.ID))
	}
	for _, item := range active {
		fmt.Fprintf(&out, "    (name = %s, external = (address = %s, http = (injectRequestHeaders = [(name = \"Authorization\", value = %s)]))),\n",
			quote(kvServiceName(item.App.ID)), quote(runtimeAddr), quote("Bearer "+item.App.RuntimeToken))
	}
	out.WriteString("  ],\n\n  sockets = [\n")
	for _, item := range active {
		fmt.Fprintf(&out, "    (name = %s, address = %s, http = (), service = %s),\n",
			quote(item.App.ID), quote(fmt.Sprintf("*:%d", item.Deployment.Port)), quote(item.App.ID))
	}
	out.WriteString("  ]\n);\n")
	for _, item := range active {
		fmt.Fprintf(&out, "\nconst %s :Workerd.Worker = (\n", workerName(item.App.ID))
		writeWorkerSource(&out, item.Deployment)
		fmt.Fprintf(&out, "  bindings = [(name = \"KV\", kvNamespace = %s)],\n", quote(kvServiceName(item.App.ID)))
		fmt.Fprintf(&out, "  compatibilityDate = %s,\n", quote(item.Deployment.CompatibilityDate))
		out.WriteString(");\n")
	}
	return out.String()
}

func writeWorkerSource(out *strings.Builder, deployment platform.Deployment) {
	if deploymentFormat(deployment) == "service-worker" {
		fmt.Fprintf(out, "  serviceWorkerScript = %s,\n", quote(deployment.Files[0].Content))
		return
	}
	out.WriteString("  modules = [\n")
	for _, file := range entrypointFirst(deployment.Files, deployment.Entrypoint) {
		fmt.Fprintf(out, "    (name = %s, esModule = %s),\n", quote(file.Path), quote(file.Content))
	}
	out.WriteString("  ],\n")
}

func deploymentFormat(deployment platform.Deployment) string {
	if deployment.Format != "" {
		return deployment.Format
	}
	if len(deployment.Files) == 1 {
		return "service-worker"
	}
	return "modules"
}

func kvServiceName(appID string) string {
	return "kv-" + appID
}

func entrypointFirst(files []platform.WorkerFile, entrypoint string) []platform.WorkerFile {
	result := make([]platform.WorkerFile, 0, len(files))
	for _, file := range files {
		if file.Path == entrypoint {
			result = append(result, file)
			break
		}
	}
	for _, file := range files {
		if file.Path != entrypoint {
			result = append(result, file)
		}
	}
	return result
}

func Traefik(active []platform.ActiveDeployment, authURL, workerHost string) string {
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
			name, yamlQuote(fmt.Sprintf("http://%s:%d/", workerHost, item.Deployment.Port)))
	}
	return out.String()
}

func writeAtomic(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
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

func quote(value string) string {
	return strconv.Quote(value)
}

func yamlQuote(value string) string {
	return strconv.Quote(value)
}
