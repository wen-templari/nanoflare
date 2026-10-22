package nanoflare

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var ErrNamespaceDeletionConflict = errors.New("namespace deletion selection changed")

type NamespaceDeletionResult struct {
	DeletedCount    *int   `json:"deleted_count,omitempty"`
	State           string `json:"nanoflare_deletion_state"`
	ConfigRefreshed bool   `json:"nanoflare_config_refreshed"`
}

func (s *Store) DeleteNamespaceScripts(org, namespace string, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ns, exists := s.dispatchNamespaces[namespace]
	if !exists || ns.OrgID != org {
		return ErrDispatchNamespaceNotFound
	}
	seen := map[string]bool{}
	for _, id := range ids {
		app, exists := s.apps[id]
		if !exists || seen[id] || app.OrgID != org || app.Platform == nil || app.Platform.NamespaceID != namespace || app.Platform.Kind != "customer" {
			return ErrNamespaceDeletionConflict
		}
		seen[id] = true
	}
	for _, id := range ids {
		if err := s.deleteAppLocked(id); err != nil {
			return err
		}
	}
	return nil
}

// Selection and publication share the same locks as tag edits and rollbacks.
// Tags select scripts; ownership and immutable membership authorize deletion.
func (s *Service) DeleteNamespaceScriptsByTag(org, namespace, name string, present bool) (NamespaceDeletionResult, error) {
	result := NamespaceDeletionResult{State: "not_started"}
	if name == "" || len(name) > 256 || strings.TrimSpace(name) != name || !utf8.ValidString(name) {
		return result, errors.New("invalid tag filter")
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	release, err := s.store.LockDeploymentUpdates()
	if err != nil {
		return result, err
	}
	defer release()
	apps, err := s.NamespaceScripts(org, namespace)
	if err != nil {
		return result, err
	}
	ids := []string{}
	selected := map[string]bool{}
	for _, app := range apps {
		if app.Platform.Kind != "customer" {
			continue
		}
		snapshot, _, err := s.ManagementUploadForOrg(org, app.ID, false)
		if err != nil {
			return result, err
		}
		found := false
		for _, tag := range snapshot.State.Metadata.Tags {
			found = found || tag == name
		}
		if found == present {
			ids = append(ids, app.ID)
			selected[app.ID] = true
		}
	}
	records, err := s.store.ListDeployments()
	if err != nil {
		return result, err
	}
	if err = s.store.DeleteNamespaceScripts(org, namespace, ids); err != nil {
		if !errors.Is(err, ErrNamespaceDeletionConflict) && !errors.Is(err, ErrDispatchNamespaceNotFound) {
			result.State = "unverified"
		}
		return result, err
	}
	count := len(ids)
	result.DeletedCount = &count
	result.State = "committed"
	for _, id := range ids {
		s.invalidateRuntimeDeployments(id)
	}
	for _, record := range records {
		if selected[record.App.ID] {
			s.cleanupDeploymentObject(record.Deployment)
			s.cleanupDeploymentAssets(record.Deployment)
		}
	}
	active, err := s.activeDeployments()
	if err != nil {
		return result, err
	}
	// Even an empty retry refreshes configuration, reconciling an earlier
	// committed deletion whose writer failed. It never recreates deleted IDs.
	if err = s.writer.Write(active); err != nil {
		return result, err
	}
	result.ConfigRefreshed = true
	return result, nil
}
