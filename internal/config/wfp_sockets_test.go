package config

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtimeassets"
)

func TestWFPSocketSettings(t *testing.T) {
	for _, input := range []string{"", " , "} {
		o, err := LoadWFPSocketOptions(input, "")
		if err != nil || len(o.Allow) != 0 {
			t.Fatalf("empty policy enables sockets: %+v %v", o, err)
		}
	}
	o, err := LoadWFPSocketOptions("public, 10.20.0.0/16, ::1", "")
	if err != nil || len(o.Allow) != 3 {
		t.Fatalf("explicit public policy: %+v %v", o, err)
	}
	for _, input := range []string{"pubic", "10.0.0.0/99", "database.example:5432", "fe80::1%eth0"} {
		if _, err := LoadWFPSocketOptions(input, ""); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	file := filepath.Join(t.TempDir(), "ca.pem")
	if _, err := LoadWFPSocketOptions("", file); err == nil {
		t.Fatal("accepted missing CA")
	}
	for _, data := range []string{"", "bad PEM", "-----BEGIN CERTIFICATE-----\nYmFk\n-----END CERTIFICATE-----\n"} {
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadWFPSocketOptions("public", file); err == nil {
			t.Fatal("accepted invalid CA")
		}
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(file, cert, 0600); err != nil {
		t.Fatal(err)
	}
	o, err = LoadWFPSocketOptions("", file)
	if err != nil || len(o.Allow) != 0 || len(o.Certificates) != 1 {
		t.Fatalf("CA alone: %+v %v", o, err)
	}
}

func TestWFPSocketConfiguration(t *testing.T) {
	active := []nanoflare.ActiveDeployment{{App: nanoflare.App{ID: "host", Name: "host"}, Deployment: nanoflare.Deployment{ID: "v1", Entrypoint: "worker.js", DispatchNamespaces: []nanoflare.DispatchBinding{{Binding: "NS", NamespaceID: "ns"}}}}}
	for _, enabled := range []bool{false, true} {
		options := WorkerdOptions{EgressAddr: "127.0.0.1:8082"}
		if enabled {
			options.WFPSockets = WFPSocketOptions{Allow: []string{"public", "10.20.0.0/16"}, Certificates: []string{"test-pem"}}
		}
		for _, generated := range []string{WorkerdWithOptions(active, options), DynamicDispatcher(options, "/tmp/test.sock", "127.0.0.1:8081", "broker", "dispatcher")} {
			binding := `(name = "__NANOFLARE_WFP_SOCKET_OUTBOUND", service = "nanoflare-wfp-sockets")`
			if strings.Contains(generated, binding) != enabled {
				t.Fatal("socket binding does not match opt-in")
			}
			if strings.Contains(generated, `name = "nanoflare-wfp-sockets"`) != enabled {
				t.Fatal("socket service does not match opt-in")
			}
			if enabled && !strings.Contains(generated, `trustedCertificates = ["test-pem"]`) {
				t.Fatal("missing socket trust store")
			}
		}
	}
	for _, writer := range []interface {
		SetWFPSockets(WFPSocketOptions)
		RuntimeOptions() WorkerdOptions
	}{NewWriter("", "", "", ""), NewRuntimeWriter("", nil)} {
		o := WFPSocketOptions{Allow: []string{"public"}, Certificates: []string{"pem"}}
		writer.SetWFPSockets(o)
		o.Allow[0] = "local"
		snapshot := writer.RuntimeOptions()
		snapshot.WFPSockets.Certificates[0] = "changed"
		if writer.RuntimeOptions().WFPSockets.Allow[0] != "public" || writer.RuntimeOptions().WFPSockets.Certificates[0] != "pem" {
			t.Fatal("socket options alias caller state")
		}
	}
}

func TestWorkerdWFPSockets(t *testing.T) {
	binary := os.Getenv("NANOFLARE_TEST_WORKERD")
	if binary == "" {
		t.Skip("set NANOFLARE_TEST_WORKERD to pinned workerd")
	}
	version, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(version)) != "workerd 2026-09-11" {
		t.Fatalf("requires pinned runtime: %s (%v)", version, err)
	}
	var service strings.Builder
	writeWFPSocketService(&service, WFPSocketOptions{Allow: []string{"local"}, AllowLocal: true, Certificates: []string{"CERTIFICATE_PLACEHOLDER"}})
	var defaultDeny strings.Builder
	writeWFPSocketService(&defaultDeny, WFPSocketOptions{Allow: []string{"127.0.0.1/32", "::1/128"}, Certificates: []string{"CERTIFICATE_PLACEHOLDER"}})
	fixture := map[string]string{"defaultDeny": defaultDeny.String(), "service": service.String(), "dispatch": runtimeassets.DispatchModule, "dynamicEnv": runtimeassets.DynamicEnvModule, "bindings": runtimeassets.BindingsModule}
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", "testdata/wfp-sockets.mjs", path, binary)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("WFP sockets: %v\n%s", err, output)
	}
	t.Log(string(output))
}

func TestWFPSocketLocalGuard(t *testing.T) {
	if _, err := LoadWFPSocketOptions("local", ""); err == nil {
		t.Fatal("local accepted without unsafe opt-in")
	}
	for _, allowLocal := range []bool{false, true} {
		o, err := LoadWFPSocketPolicy("private,127.0.0.1", "10.0.0.0/8", "", allowLocal)
		if err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		writeWFPSocketService(&out, o)
		if strings.Contains(out.String(), `"local"`) == allowLocal {
			t.Fatal("default local deny does not follow unsafe opt-in")
		}
		if !strings.Contains(out.String(), `deny = ["10.0.0.0/8"`) {
			t.Fatal("missing explicit deny")
		}
	}
	if _, err := LoadWFPSocketPolicy("public", "not-a-range", "", false); err == nil {
		t.Fatal("invalid deny accepted")
	}
	if _, err := LoadWFPSocketPolicy("local", "", "", true); err != nil {
		t.Fatal(err)
	}
}

func TestWFPSocketPolicyLogging(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	defer slog.SetDefault(previous)
	LogWFPSocketPolicy(WFPSocketOptions{})
	if !strings.Contains(output.String(), `"enabled":false`) {
		t.Fatal("missing disabled policy log")
	}
	output.Reset()
	LogWFPSocketPolicy(WFPSocketOptions{Allow: []string{"private"}, AllowLocal: true, Certificates: []string{"secret-certificate-content"}})
	for _, expected := range []string{`"enabled":true`, `"additional_ca_count":1`, "private policy", "local access explicitly enabled"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %s in log", expected)
		}
	}
	if strings.Contains(output.String(), "secret-certificate-content") {
		t.Fatal("logged certificate contents")
	}
}
