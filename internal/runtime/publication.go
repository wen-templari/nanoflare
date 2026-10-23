package runtime

import "github.com/clas/nanoflare/internal/nanoflare"

func (m *Manager) PreparePublication(active []nanoflare.ActiveDeployment) (nanoflare.PublicationPreparation, error) {
	generation, routed, err := m.Prepare(active)
	return nanoflare.PublicationPreparation{Generation: generation, Deployments: routed}, err
}
func (m *Manager) CommitPublication(p nanoflare.PublicationPreparation) error {
	if err := m.writer.WriteTraefik(p.Deployments); err != nil {
		return err
	}
	return m.Commit(p.Generation)
}
func (m *Manager) AbortPublication(p nanoflare.PublicationPreparation) error {
	return m.Abort(p.Generation)
}

func (m *DynamicManager) PreparePublication(active []nanoflare.ActiveDeployment) (nanoflare.PublicationPreparation, error) {
	generation, routed, err := m.Prepare(active)
	return nanoflare.PublicationPreparation{Generation: generation, Deployments: routed}, err
}
func (m *DynamicManager) CommitPublication(p nanoflare.PublicationPreparation) error {
	if err := m.writer.WriteTraefik(p.Deployments); err != nil {
		return err
	}
	return m.Commit(p.Generation)
}
func (m *DynamicManager) AbortPublication(p nanoflare.PublicationPreparation) error {
	return m.Abort(p.Generation)
}

// ValidatePublication supports compatibility runners without publishing routes.
func (m *Manager) ValidatePublication(active []nanoflare.ActiveDeployment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	generation, _, err := m.prepare(active)
	if err != nil {
		return err
	}
	if m.pending == nil {
		return nil
	}
	return m.abort(generation)
}

// PrepareRestore restores persisted routing while isolating invalid revisions.
func (m *DynamicManager) PrepareRestore(active []nanoflare.ActiveDeployment) (string, []nanoflare.ActiveDeployment, error) {
	m.publication.Lock()
	defer m.publication.Unlock()
	return m.prepareDynamic(active, true, "")
}

// PrepareWorkerPublication strictly validates the target Worker while retaining
// unavailable, unchanged revisions belonging to other Workers.
func (m *DynamicManager) PrepareWorkerPublication(active []nanoflare.ActiveDeployment, appID string) (nanoflare.PublicationPreparation, error) {
	m.publication.Lock()
	defer m.publication.Unlock()
	id, routed, err := m.prepareDynamic(active, false, appID)
	if err != nil {
		m.collectSnapshots()
	}
	return nanoflare.PublicationPreparation{Generation: id, Deployments: routed}, err
}

// PrepareRestore preserves the legacy manager's strict startup behavior.
func (m *Manager) PrepareRestore(active []nanoflare.ActiveDeployment) (string, []nanoflare.ActiveDeployment, error) {
	return m.Prepare(active)
}
