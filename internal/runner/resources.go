package runner

import (
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func readRunnerResources() *nanoflare.RunnerResources {
	if goruntime.GOOS != "linux" {
		return nil
	}
	snapshot, err := readCgroupResources("/proc/self/cgroup", "/sys/fs/cgroup")
	if err != nil {
		return nil
	}
	return snapshot
}

// Resolve the process's own unified cgroup. Reading only the filesystem mount
// root would incorrectly report the whole host on an ordinary systemd host.
func readCgroupResources(membership, mount string) (*nanoflare.RunnerResources, error) {
	data, err := os.ReadFile(membership)
	if err != nil {
		return nil, err
	}
	group := ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "0::/") {
			group = strings.TrimPrefix(line, "0::")
			break
		}
	}
	if group == "" {
		return nil, errors.New("unified cgroup unavailable")
	}
	for _, part := range strings.Split(group, "/") {
		if part == "." || part == ".." {
			return nil, errors.New("cgroup path outside visible hierarchy")
		}
	}
	directory := filepath.Join(mount, strings.TrimPrefix(group, "/"))
	number := func(name string, allowMax bool) (uint64, bool, error) {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return 0, false, err
		}
		value := strings.TrimSpace(string(data))
		if allowMax && value == "max" {
			return 0, false, nil
		}
		n, err := strconv.ParseUint(value, 10, 64)
		return n, true, err
	}
	result := &nanoflare.RunnerResources{}
	result.MemoryBytes, _, err = number("memory.current", false)
	if err != nil {
		return nil, err
	}
	result.MemoryLimitBytes, result.MemoryLimited, err = number("memory.max", true)
	if err != nil {
		return nil, err
	}
	result.PIDs, _, err = number("pids.current", false)
	if err != nil {
		return nil, err
	}
	result.PIDLimit, result.PIDLimited, err = number("pids.max", true)
	if err != nil {
		return nil, err
	}
	result.MemoryPeakBytes, result.MemoryPeakAvailable, err = number("memory.peak", false)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	events, err := os.ReadFile(filepath.Join(directory, "memory.events"))
	if err != nil {
		return nil, err
	}
	found := false
	for _, line := range strings.Split(string(events), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "oom_kill" {
			result.OOMKills, err = strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return nil, err
			}
			found = true
		}
	}
	if !found {
		return nil, errors.New("OOM accounting unavailable")
	}
	return result, nil
}
