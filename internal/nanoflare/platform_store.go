package nanoflare

import (
	"sort"
	"time"
)

func cloneNamespace(ns DispatchNamespace) DispatchNamespace {
	ns.Grants = append([]NamespaceResourceGrant{}, ns.Grants...)
	return ns
}
func (s *Store) CreateDispatchNamespace(ns DispatchNamespace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.dispatchNamespaces {
		if n.ID == ns.ID || (n.OrgID == ns.OrgID && n.Name == ns.Name) {
			return ErrDispatchNamespaceExists
		}
	}
	s.dispatchNamespaces[ns.ID] = cloneNamespace(ns)
	s.namespacePolicies[ns.ID] = []NamespacePolicy{{NamespaceID: ns.ID, Revision: 1, Grants: append([]NamespaceResourceGrant{}, ns.Grants...), CreatedAt: ns.CreatedAt}}
	return nil
}
func (s *Store) GetDispatchNamespace(id string) (DispatchNamespace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.dispatchNamespaces[id]
	if !ok {
		return n, ErrDispatchNamespaceNotFound
	}
	return cloneNamespace(n), nil
}
func (s *Store) ListDispatchNamespaces(org string) ([]DispatchNamespace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []DispatchNamespace{}
	for _, ns := range s.dispatchNamespaces {
		if ns.OrgID == org {
			out = append(out, cloneNamespace(ns))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (s *Store) RenameDispatchNamespace(id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.dispatchNamespaces[id]
	if !ok {
		return ErrDispatchNamespaceNotFound
	}
	for _, ns := range s.dispatchNamespaces {
		if ns.ID != id && ns.OrgID == n.OrgID && ns.Name == name {
			return ErrDispatchNamespaceExists
		}
	}
	n.Name = name
	s.dispatchNamespaces[id] = n
	return nil
}
func (s *Store) DeleteDispatchNamespace(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ns, ok := s.dispatchNamespaces[id]
	if !ok {
		return ErrDispatchNamespaceNotFound
	}
	if len(ns.Grants) > 0 {
		return ErrDispatchNamespaceInUse
	}
	for _, a := range s.apps {
		if a.Platform != nil && a.Platform.NamespaceID == id {
			return ErrDispatchNamespaceInUse
		}
	}
	delete(s.dispatchNamespaces, id)
	delete(s.namespacePolicies, id)
	return nil
}
func (s *Store) SetNamespaceGrants(id string, revision int64, grants []NamespaceResourceGrant) (DispatchNamespace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ns, ok := s.dispatchNamespaces[id]
	if !ok {
		return ns, ErrDispatchNamespaceNotFound
	}
	if ns.PolicyRevision != revision {
		return ns, ErrNamespacePolicyConflict
	}
	ns.PolicyRevision++
	ns.Grants = append([]NamespaceResourceGrant{}, grants...)
	s.dispatchNamespaces[id] = ns
	s.namespacePolicies[id] = append(s.namespacePolicies[id], NamespacePolicy{NamespaceID: id, Revision: ns.PolicyRevision, Grants: append([]NamespaceResourceGrant{}, grants...), CreatedAt: time.Now().UTC()})
	return cloneNamespace(ns), nil
}
func (s *Store) NamespacePolicies(id string) ([]NamespacePolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]NamespacePolicy{}, s.namespacePolicies[id]...)
	for i := range out {
		out[i].Grants = append([]NamespaceResourceGrant{}, out[i].Grants...)
	}
	return out, nil
}
