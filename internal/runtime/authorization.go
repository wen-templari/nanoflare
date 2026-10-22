package runtime

import "context"

// RuntimeDeploymentLive includes prepared and draining generations only while
// their process is live. stop/restart/abort revoke the corresponding grants.
func (m *Manager) RuntimeDeploymentLive(ctx context.Context, appID, deploymentID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.liveMu.RLock()
	defer m.liveMu.RUnlock()
	for p := range m.livePools {
		select {
		case <-p.exited:
			continue
		default:
		}
		for _, item := range p.active {
			if item.App.ID == appID && item.Deployment.ID == deploymentID {
				return true, nil
			}
		}
	}
	return false, nil
}

func (m *Manager) RuntimeInstanceLive(ctx context.Context, appID, deploymentID, instanceID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.liveMu.RLock()
	defer m.liveMu.RUnlock()
	for p := range m.livePools {
		select {
		case <-p.exited:
			continue
		default:
		}
		for _, item := range p.active {
			if item.App.ID == appID && item.Deployment.ID == deploymentID && item.RuntimeInstance == instanceID {
				return true, nil
			}
		}
	}
	return false, nil
}
