package runtime

import (
	"context"
	"fmt"
	"sort"

	"github.com/clas/nanoflare/internal/nanoflare"
)

// WorkerUnavailableError identifies an isolated revision initialization failure.
// It deliberately excludes runtime exception text, source, and binding values.
type WorkerUnavailableError struct{ AppID, DeploymentID string }

func (e *WorkerUnavailableError) Error() string {
	return fmt.Sprintf("worker_initialization_failed: Worker %s revision %s is unavailable", e.AppID, e.DeploymentID)
}

// RuntimeStatusReader returns only committed revision observations, never secrets.
type RuntimeStatusReader interface {
	RuntimeStatuses(context.Context, string) ([]nanoflare.RuntimeRevisionStatus, error)
}

func (m *DynamicManager) RuntimeStatuses(ctx context.Context, appID string) ([]nanoflare.RuntimeRevisionStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]nanoflare.RuntimeRevisionStatus, 0)
	for _, active := range m.active {
		if appID != "" && active.App.ID != appID {
			continue
		}
		state := nanoflare.RuntimeRevisionStatus{AppID: active.App.ID, DeploymentID: active.Deployment.ID, State: "ready"}
		snapshot := m.snapshots[m.committed[dynamicIdentity(active)]]
		if snapshot != nil && snapshot.validationFailed {
			state.State, state.Reason = "unavailable", "worker_initialization_failed"
		} else if snapshot == nil || !snapshot.validated || m.closed || m.process == nil || m.session == "" {
			state.State, state.Reason = "unavailable", "runtime_unavailable"
		}
		result = append(result, state)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].AppID != result[j].AppID {
			return result[i].AppID < result[j].AppID
		}
		return result[i].DeploymentID < result[j].DeploymentID
	})
	return result, nil
}
