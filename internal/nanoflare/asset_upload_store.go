package nanoflare

import (
	"context"
	"encoding/json"
	"time"
)

func cloneAssetUpload(value AssetUploadSession) AssetUploadSession {
	data, _ := json.Marshal(value)
	var result AssetUploadSession
	_ = json.Unmarshal(data, &result)
	return result
}
func (s *Store) GetAssetUploadSession(id string) (AssetUploadSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.assetUploads[id]
	if !ok {
		return AssetUploadSession{}, ErrAssetUploadUnavailable
	}
	return cloneAssetUpload(value), nil
}
func (s *Store) SaveAssetUploadSession(value AssetUploadSession, expected int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assetUploads == nil {
		s.assetUploads = map[string]AssetUploadSession{}
	}
	old, exists := s.assetUploads[value.ID]
	if value.Revision != expected+1 || (exists && old.Revision != expected) || (!exists && expected != 0) {
		return ErrManagementConflict
	}
	if exists && (old.OrgID != value.OrgID || old.NamespaceID != value.NamespaceID) {
		return ErrManagementConflict
	}
	ns, exists := s.dispatchNamespaces[value.NamespaceID]
	if !exists || ns.OrgID != value.OrgID {
		return ErrDispatchNamespaceNotFound
	}
	s.assetUploads[value.ID] = cloneAssetUpload(value)
	return nil
}
func (s *Store) CountAssetUploadSessions(org string, now time.Time) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, session := range s.assetUploads {
		if !session.ExpiresAt.After(now) {
			continue
		}
		if _, exists := s.dispatchNamespaces[session.NamespaceID]; !exists {
			continue
		}
		if session.OrgID == org {
			count++
		}
	}
	return count, nil
}

func (s *Store) PruneAssetUploadSessions(ctx context.Context, now time.Time, limit int) (int, error) {
	if err := validateAssetPruneLimit(limit); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for id, session := range s.assetUploads {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		_, exists := s.dispatchNamespaces[session.NamespaceID]
		if !session.ExpiresAt.After(now) || !exists {
			delete(s.assetUploads, id)
			count++
			if count == limit {
				break
			}
		}
	}
	return count, nil
}
