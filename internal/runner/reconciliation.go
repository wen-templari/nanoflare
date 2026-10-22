package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/clas/nanoflare/internal/nanoflare"
	"net/http"
	"time"
)

// BootID identifies the runner process, not runtime readiness or a capability.
// It changes when the runner loses its volatile generation state.
func (c *Client) BootID(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/status", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", fmt.Errorf("runner status: HTTP %d", response.StatusCode)
	}
	var status struct {
		BootID    string          `json:"boot_id"`
		Resources json.RawMessage `json:"resources"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, response.Body, 4096)).Decode(&status); err != nil {
		return "", err
	}
	if status.BootID == "" {
		return "", errors.New("runner status missing boot identity")
	}
	// Optional resource telemetry cannot prevent boot recovery. A bad/missing
	// measurement clears availability instead of being reported as zero usage.
	var resources *nanoflare.RunnerResources
	if len(status.Resources) > 0 {
		var decoded nanoflare.RunnerResources
		var fields map[string]json.RawMessage
		complete := json.Unmarshal(status.Resources, &fields) == nil
		for _, name := range []string{"memory_bytes", "memory_limit_bytes", "memory_limited", "pids", "pid_limit", "pid_limited", "oom_kills"} {
			if _, ok := fields[name]; !ok {
				complete = false
			}
		}
		if complete && json.Unmarshal(status.Resources, &decoded) == nil && decoded.MemoryBytes > 0 && decoded.PIDs > 0 && (!decoded.MemoryLimited || decoded.MemoryLimitBytes > 0) && (!decoded.PIDLimited || decoded.PIDLimit > 0) {
			resources = &decoded
		}
	}
	c.resources.Store(&resourceObservation{resources: resources, at: time.Now()})
	return status.BootID, nil
}

// StartReconciliation repairs a fresh/restarted remote runner. A changed boot
// identity is acknowledged only after authoritative reconciliation succeeds;
// failures retry on the next tick. Polling an unchanged process never republishes
// a generation. The callback must serialize snapshots with other publications.
// Stop joins the callback before its repository/runtime dependencies are closed.
func (c *Client) StartReconciliation(parent context.Context, interval time.Duration, reconcile func() error, onError func(error)) func() {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var acknowledged string
		for {
			if ctx.Err() != nil {
				return
			}
			boot, err := c.BootID(ctx)
			if err == nil && boot != acknowledged && ctx.Err() == nil {
				err = reconcile()
				if err == nil {
					acknowledged = boot
				}
			}
			if err != nil && ctx.Err() == nil && onError != nil {
				onError(err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

type resourceObservation struct {
	resources *nanoflare.RunnerResources
	at        time.Time
}

// Resources returns a copy of the last authenticated status observation. The
// exporter checks its age; a failed poll does not renew the measurement.
func (c *Client) Resources() (*nanoflare.RunnerResources, time.Time) {
	observation := c.resources.Load()
	if observation == nil {
		return nil, time.Time{}
	}
	if observation.resources == nil {
		return nil, observation.at
	}
	value := *observation.resources
	return &value, observation.at
}
