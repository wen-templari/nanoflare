package nanoflare

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

type ResourceGrantState string

const (
	ResourceGrantPrepared  ResourceGrantState = "prepared"
	ResourceGrantPublished ResourceGrantState = "published"
	ResourceGrantDraining  ResourceGrantState = "draining"
	ResourceGrantRevoked   ResourceGrantState = "revoked"
)

// Runner sessions and resource grants are ephemeral. Snapshot identity describes
// content; each grant is a fresh, non-revivable generation of authority.
func (r *ExecutionRegistry) RenewResourceSession(session string, expires time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[session] = expires
}

func (r *ExecutionRegistry) sessionLiveLocked(session string) bool {
	return r.sessions[session].After(time.Now())
}

func (r *ExecutionRegistry) PrepareResourceGrant(snapshot *ExecutionSnapshot, session, registration string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if registration == "" || len(registration) > 256 || !r.sessionLiveLocked(session) {
		return "", ErrInvalidCapability
	}
	if r.registrations[session] == nil {
		r.registrations[session] = map[string]string{}
	}
	if id := r.registrations[session][registration]; id != "" {
		revision := r.revisions[id]
		if revision == nil || revision.snapshot.ID() != snapshot.ID() || revision.state == ResourceGrantRevoked || revision.state == ResourceGrantDraining {
			return "", ErrExecutionRetired
		}
		return id, nil
	}
	id := ResourceGrantID(session, registration)
	if r.revokedGrants[session][id] {
		return "", ErrExecutionRetired
	}
	r.revisions[id] = &executionRevision{snapshot: snapshot, key: id, session: session, state: ResourceGrantPrepared}
	r.registrations[session][registration] = id
	return id, nil
}

func (r *ExecutionRegistry) PublishResourceGrant(session, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	revision := r.revisions[id]
	if revision == nil || revision.session != session || !r.sessionLiveLocked(session) {
		return ErrInvalidCapability
	}
	if revision.state != ResourceGrantPrepared && revision.state != ResourceGrantPublished {
		return ErrExecutionRetired
	}
	revision.state, revision.accepting = ResourceGrantPublished, true
	return nil
}

func (r *ExecutionRegistry) RetireResourceGrant(session, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if revision := r.revisions[id]; revision != nil && revision.session != session {
		return ErrInvalidCapability
	}
	if !r.sessionLiveLocked(session) {
		return nil
	}
	if r.revokedGrants[session] == nil {
		r.revokedGrants[session] = map[string]bool{}
	}
	r.revokedGrants[session][id] = true
	revision := r.revisions[id]
	if revision == nil {
		return nil
	}
	if revision.session != session {
		return ErrInvalidCapability
	}
	revision.state, revision.accepting = ResourceGrantDraining, false
	r.collectRevisionLocked(revision)
	return nil
}

func (r *ExecutionRegistry) collectRevisionLocked(revision *executionRevision) {
	if revision.accepting || revision.state == ResourceGrantPrepared {
		return
	}
	if revision.leases == 0 {
		if revision.state != "" {
			revision.state = ResourceGrantRevoked
		}
		if revision.operations == 0 {
			delete(r.revisions, revision.key)
		}
	}
}

// AcquireResource pins an accepted operation independently of the originating
// invocation. The returned token is scoped to that operation and stays internal
// to the resource HTTP handler until its response, including streaming, finishes.
func (r *ExecutionRegistry) AcquireResource(grant string, kind ExecutionResourceKind, resourceID string) (*ExecutionLease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	revision := r.revisions[grant]
	if revision == nil || !r.sessionLiveLocked(revision.session) ||
		(revision.state != ResourceGrantPublished && !(revision.state == ResourceGrantDraining && revision.leases > 0)) {
		return nil, ErrInvalidCapability
	}
	token := "nfexec_" + rand.Text()
	r.tokens[token] = &executionAdmission{revision: revision, pins: 1, operation: true, resourceKind: kind, resourceID: resourceID}
	if _, err := r.authorizeLocked(token, kind, resourceID); err != nil {
		delete(r.tokens, token)
		return nil, err
	}
	revision.operations++
	return &ExecutionLease{registry: r, token: token}, nil
}

func (r *ExecutionRegistry) RevokeResourceSession(session string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, session)
	delete(r.registrations, session)
	delete(r.revokedGrants, session)
	for token, admission := range r.tokens {
		if admission.revision.session == session && !admission.operation {
			admission.revision.leases--
			delete(r.tokens, token)
		}
	}
	for _, revision := range r.revisions {
		if revision.session == session {
			revision.state, revision.accepting = ResourceGrantRevoked, false
			r.collectRevisionLocked(revision)
		}
	}
}

// A caller-chosen registration nonce makes uncertain registrations cancelable.
// The key is unrelated to the content hash and unique across runner sessions.
func ResourceGrantID(session, registration string) string {
	digest := sha256.Sum256([]byte(session + "\x00" + registration))
	return "nfgrant_" + hex.EncodeToString(digest[:])
}
