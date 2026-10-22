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
