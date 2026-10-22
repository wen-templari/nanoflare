package runner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCgroupResourcesUseOwnedHierarchyAndUnknownIsUnavailable(t *testing.T) {
	root := t.TempDir()
	membership := filepath.Join(root, "membership")
	mount := filepath.Join(root, "cgroup")
	group := filepath.Join(mount, "system.slice", "runner.service")
	if err := os.MkdirAll(group, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(membership, "0::/system.slice/runner.service\n")
	for name, value := range map[string]string{"memory.current": "65536", "memory.max": "1073741824", "memory.peak": "131072", "pids.current": "18", "pids.max": "128", "memory.events": "low 0\nhigh 0\nmax 2\noom 1\noom_kill 1\n"} {
		write(filepath.Join(group, name), value)
	}
	// A wrong mount-root lookup would report these unrelated host measurements.
	write(filepath.Join(mount, "memory.current"), "999999999")
	snapshot, err := readCgroupResources(membership, mount)
	if err != nil || snapshot.MemoryBytes != 65536 || snapshot.PIDs != 18 || snapshot.OOMKills != 1 || !snapshot.MemoryLimited || snapshot.MemoryLimitBytes != 1<<30 || !snapshot.PIDLimited || snapshot.PIDLimit != 128 || !snapshot.MemoryPeakAvailable || snapshot.MemoryPeakBytes != 131072 {
		t.Fatalf("wrong physical group: %+v %v", snapshot, err)
	}
	write(filepath.Join(group, "memory.max"), "max")
	write(filepath.Join(group, "pids.max"), "max")
	if err := os.Remove(filepath.Join(group, "memory.peak")); err != nil {
		t.Fatal(err)
	}
	snapshot, err = readCgroupResources(membership, mount)
	if err != nil || snapshot.MemoryLimited || snapshot.PIDLimited || snapshot.MemoryPeakAvailable {
		t.Fatalf("unlimited/missing peak misrepresented: %+v %v", snapshot, err)
	}
	write(filepath.Join(group, "memory.current"), "invalid")
	if snapshot, err = readCgroupResources(membership, mount); err == nil || snapshot != nil {
		t.Fatal("invalid measurement became zero usage")
	}
	write(membership, "0::/../../other\n")
	if snapshot, err = readCgroupResources(membership, mount); err == nil || snapshot != nil {
		t.Fatal("escaped cgroup hierarchy accepted")
	}
	write(membership, "2:memory:/legacy\n")
	if snapshot, err = readCgroupResources(membership, mount); err == nil || snapshot != nil {
		t.Fatal("legacy hierarchy treated as measured unified cgroup")
	}
}

func TestBadResourceTelemetryDoesNotPreventBootRecovery(t *testing.T) {
	const valid = `{"memory_bytes":100,"pids":2,"memory_limit_bytes":0,"memory_limited":false,"pid_limit":0,"pid_limited":false,"oom_kills":0}`
	for _, resources := range []string{`null`, `{}`, `{"memory_bytes":-1}`, `{"memory_bytes":100,"pids":2}`, valid} {
		t.Run(resources, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"boot_id":"new-boot","resources":%s}`, resources)
			}))
			defer server.Close()
			client := NewClient(server.URL, "fixture", nil)
			boot, err := client.BootID(context.Background())
			if err != nil || boot != "new-boot" {
				t.Fatalf("telemetry prevented recovery: %s %v", boot, err)
			}
			snapshot, at := client.Resources()
			if at.IsZero() {
				t.Fatal("authenticated status observation not recorded")
			}
			if resources == valid {
				if snapshot == nil || snapshot.MemoryBytes != 100 {
					t.Fatal("valid physical sample missing")
				}
				snapshot.MemoryBytes = 999
				again, _ := client.Resources()
				if again.MemoryBytes != 100 {
					t.Fatal("caller mutated cached resource sample")
				}
			} else if snapshot != nil {
				t.Fatal("bad or absent measurement became available")
			}
		})
	}
}
