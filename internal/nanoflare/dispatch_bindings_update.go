package nanoflare

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidDispatchBindingsUpdate = errors.New("invalid dispatch bindings update")

// UpdateDispatchBindingsInput replaces dispatch bindings while retaining the
// current code, assets, variables and exact frozen secret references.
type UpdateDispatchBindingsInput struct {
	DispatchNamespaces   []DispatchBinding `json:"dispatch_namespaces" doc:"Complete replacement list; an empty array removes all dispatch bindings."`
	ExpectedDeploymentID string            `json:"expected_deployment_id" minLength:"1" maxLength:"256" doc:"Must be the sole active deployment receiving 100% of traffic."`
	IdempotencyKey       string            `json:"idempotency_key" minLength:"1" maxLength:"256" doc:"Worker-scoped retry key; identical retries return the original result without reactivation."`
	CommitMessage        string            `json:"commit_message,omitempty" maxLength:"4096"`
	CreatedBy            string            `json:"-"`
}

func (input *UpdateDispatchBindingsInput) UnmarshalJSON(data []byte) error {
	type alias UpdateDispatchBindingsInput
	var value alias
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if value.DispatchNamespaces == nil {
		return fmt.Errorf("%w: dispatch_namespaces must be an array", ErrInvalidDispatchBindingsUpdate)
	}
	*input = UpdateDispatchBindingsInput(value)
	return nil
}

func (s *Service) UpdateDispatchBindingsForOrg(orgID, appID string, input UpdateDispatchBindingsInput) (VarsUpdateResult, error) {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return VarsUpdateResult{}, err
	}
	input.ExpectedDeploymentID = strings.TrimSpace(input.ExpectedDeploymentID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.CommitMessage = strings.TrimSpace(input.CommitMessage)
	if input.DispatchNamespaces == nil || input.ExpectedDeploymentID == "" || len(input.ExpectedDeploymentID) > 256 || input.IdempotencyKey == "" || len(input.IdempotencyKey) > 256 || len(input.CommitMessage) > 4096 {
		return VarsUpdateResult{}, fmt.Errorf("%w: dispatch_namespaces, expected_deployment_id and idempotency_key are required", ErrInvalidDispatchBindingsUpdate)
	}
	input.DispatchNamespaces = CloneDispatchBindings(input.DispatchNamespaces)
	payload, err := json.Marshal(input)
	if err != nil {
		return VarsUpdateResult{}, err
	}
	fingerprint := sha256.Sum256(append([]byte("dispatch-bindings:"), payload...))
	// The durable publication journal is shared with vars updates. Hashing an
	// operation-specific namespace prevents a retry key from reaching a vars result.
	keyHash := sha256.Sum256([]byte("dispatch-bindings:" + input.IdempotencyKey))
	publication := UpdateVarsInput{ExpectedDeploymentID: input.ExpectedDeploymentID, IdempotencyKey: "dispatch-bindings:" + hex.EncodeToString(keyHash[:]), CommitMessage: input.CommitMessage, CreatedBy: input.CreatedBy}
	return s.updateRetainedDeploymentForOrg(orgID, appID, publication, hex.EncodeToString(fingerprint[:]), func(current *ActiveDeployment, next *Deployment) error {
		if current.App.IsDynamic() {
			return fmt.Errorf("%w: only ordinary Workers can host dispatch namespaces", ErrInvalidDispatchBindingsUpdate)
		}
		next.DispatchNamespaces = CloneDispatchBindings(input.DispatchNamespaces)
		validation := DeployInput{Files: next.Files, Entrypoint: next.Entrypoint, Format: next.Format, CompatibilityFlags: next.CompatibilityFlags, Vars: next.Vars, KVNamespaces: next.KVNamespaces, Databases: next.Databases, ObjectStorageBuckets: next.ObjectStorageBuckets, Services: next.Services, AssetConfig: next.AssetConfig, DNS: next.DNS, Triggers: next.Triggers, DispatchNamespaces: next.DispatchNamespaces}
		if err := s.validateDispatchBindings(current.App, validation); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidDispatchBindingsUpdate, err)
		}
		return nil
	})
}
