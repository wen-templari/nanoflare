package config

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Keep this engine-level gate independent of management API acceptance. A
// policy must not become configurable until native networking can be isolated
// without putting request-local capabilities into a cached code descriptor.
func TestWorkerdOutboundIsolation(t *testing.T) {
	binary := os.Getenv("NANOFLARE_TEST_WORKERD")
	if binary == "" {
		t.Skip("set NANOFLARE_TEST_WORKERD to pinned workerd")
	}
	version, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(version)) != "workerd 2026-09-11" {
		t.Fatalf("requires pinned runtime: %s (%v)", version, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "testdata/outbound-workers.mjs", binary).CombinedOutput()
	if err != nil {
		t.Fatalf("outbound isolation: %v\n%s", err, output)
	}
	t.Log(string(output))
}
