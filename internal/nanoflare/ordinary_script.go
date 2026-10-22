package nanoflare

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// Ordinary script names have their own account-scoped identity. Native display
// names are deliberately not adopted: they are not unique and may be unrelated.
func ordinaryScriptID(org, name string) (string, error) {
	if org == "" || strings.TrimSpace(org) != org || !platformName.MatchString(name) {
		return "", errors.New("script name must be 1-63 lowercase letters, digits, underscores or hyphens")
	}
	sum := sha256.Sum256([]byte("nanoflare-ordinary-script-v1\x00" + org + "\x00" + name))
	return "cf-worker-" + hex.EncodeToString(sum[:]), nil
}

func (s *Service) OrdinaryScriptForOrg(org, name string) (App, error) {
	id, err := ordinaryScriptID(org, name)
	if err != nil {
		return App{}, err
	}
	app, err := s.appForOrg(org, id)
	if err == nil && (app.Platform != nil || app.Name != name) {
		return App{}, ErrAppNotFound
	}
	return app, err
}

// EnsureOrdinaryScriptForOrg serializes identity creation with publication and
// deletion across service instances. The database primary key is the final
// uniqueness constraint. Upload does not implicitly publish a hostname or TLS.
func (s *Service) EnsureOrdinaryScriptForOrg(org, name, actor string) (App, error) {
	id, err := ordinaryScriptID(org, name)
	if err != nil {
		return App{}, err
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	release, err := s.store.LockDeploymentUpdates()
	if err != nil {
		return App{}, err
	}
	defer release()
	app, err := s.OrdinaryScriptForOrg(org, name)
	if !errors.Is(err, ErrAppNotFound) {
		return app, err
	}
	if _, err := s.store.GetOrganization(org); err != nil {
		return App{}, err
	}
	if err := s.enforceOrgLimit(org, "worker"); err != nil {
		return App{}, err
	}
	token, err := randomToken()
	if err != nil {
		return App{}, err
	}
	app = App{ID: id, OrgID: org, Name: name, RuntimeToken: token, CreatedBy: actor, CreatedAt: time.Now().UTC()}
	return app, s.store.CreateApp(app)
}
