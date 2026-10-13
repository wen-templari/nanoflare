package nanoflare

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func normalizeVarsUpdate(input UpdateVarsInput) (UpdateVarsInput, string, error) {
	input.ExpectedDeploymentID = strings.TrimSpace(input.ExpectedDeploymentID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.CommitMessage = strings.TrimSpace(input.CommitMessage)
	if input.Vars == nil || len(input.ExpectedDeploymentID) == 0 || len(input.ExpectedDeploymentID) > 256 || len(input.IdempotencyKey) == 0 || len(input.IdempotencyKey) > 256 || len(input.CommitMessage) > 4096 {
		return input, "", fmt.Errorf("%w: vars object, expected_deployment_id and idempotency_key are required; IDs/keys max 256 bytes, message max 4096 bytes", ErrInvalidVarsUpdate)
	}
	vars, err := normalizeVars(input.Vars)
	if err != nil {
		return input, "", fmt.Errorf("%w: %v", ErrInvalidVarsUpdate, err)
	}
	// Canonicalize nested object order and whitespace without rounding JSON numbers.
	for name, raw := range vars {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return input, "", fmt.Errorf("%w: %v", ErrInvalidVarsUpdate, err)
		}
		vars[name], err = json.Marshal(value)
		if err != nil {
			return input, "", fmt.Errorf("%w: %v", ErrInvalidVarsUpdate, err)
		}
	}
	input.Vars = vars
	payload, err := json.Marshal(input) // CreatedBy is deliberately excluded.
	if err != nil {
		return input, "", err
	}
	hash := sha256.Sum256(payload)
	return input, hex.EncodeToString(hash[:]), nil
}

func (s *Service) UpdateVarsForOrg(orgID, appID string, input UpdateVarsInput) (VarsUpdateResult, error) {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return VarsUpdateResult{}, err
	}
	input, fingerprint, err := normalizeVarsUpdate(input)
	if err != nil {
		return VarsUpdateResult{}, err
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	release, err := s.store.LockDeploymentUpdates()
	if err != nil {
		return VarsUpdateResult{}, err
	}
	defer release()
	// Recheck ownership after taking the lock (the worker may have been deleted).
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return VarsUpdateResult{}, err
	}
	op, found, err := s.store.VarsUpdate(appID, input.IdempotencyKey)
	if err != nil {
		return VarsUpdateResult{}, err
	}
	if found {
		if op.Fingerprint != fingerprint {
			return VarsUpdateResult{}, ErrIdempotencyConflict
		}
		if op.Status == "failed" {
			return VarsUpdateResult{}, ErrVarsRolloutFailed
		}
		if op.Status == "rolling_back" {
			if err := s.recoverVarsRollback(op); err != nil {
				return VarsUpdateResult{}, err
			}
			return VarsUpdateResult{}, ErrVarsRolloutFailed
		}
		if op.Status == "pending" {
			// Activation and pending outcome are committed together. After a restart,
			// reconcile current configuration, never reactivate an old revision.
			records, err := s.store.ListDeployments()
			if err != nil {
				return VarsUpdateResult{}, err
			}
			retained := false
			for _, record := range records {
				if record.Deployment.AppID == appID && record.Deployment.ID == op.Result.ID {
					retained = true
					break
				}
			}
			if !retained {
				// Reconcile routing before saving a terminal outcome, so a failed
				// restoration can still be recovered by another identical retry.
				if err := s.publishActiveDeployments(appID); err != nil {
					return VarsUpdateResult{}, err
				}
				if err := s.store.FinishVarsUpdate(appID, op.Key, "failed"); err != nil {
					return VarsUpdateResult{}, err
				}
				return VarsUpdateResult{}, ErrVarsRolloutFailed
			}
			if err := s.publishActiveDeployments(appID); err != nil {
				return VarsUpdateResult{}, err
			}
			if err := s.store.FinishVarsUpdate(appID, op.Key, "succeeded"); err != nil {
				return VarsUpdateResult{}, err
			}
		}
		return op.Result, nil
	}
	active, err := s.activeDeployments()
	if err != nil {
		return VarsUpdateResult{}, err
	}
	traffic := activeTrafficForApp(active, appID)
	if len(traffic) != 1 || traffic[0].TrafficPercent != 100 || traffic[0].ID != input.ExpectedDeploymentID {
		return VarsUpdateResult{}, ErrDeploymentConflict
	}
	current := activeForApp(active, appID)
	if err := validateBindingCollisions(input.Vars, current.App.SecretValues, current.Deployment.KVNamespaces, current.Deployment.Databases, current.Deployment.ObjectStorageBuckets, current.Deployment.Services, current.Deployment.AssetConfig); err != nil {
		return VarsUpdateResult{}, fmt.Errorf("%w: %v", ErrInvalidVarsUpdate, err)
	}
	// Round-trip to detach every slice/map, including nested trigger/asset config.
	payload, err := json.Marshal(current.Deployment)
	if err != nil {
		return VarsUpdateResult{}, err
	}
	var next Deployment
	if err := json.Unmarshal(payload, &next); err != nil {
		return VarsUpdateResult{}, err
	}
	next.ObjectKey = current.Deployment.ObjectKey
	next.BundleSize = current.Deployment.BundleSize
	next.ID, err = randomToken()
	if err != nil {
		return VarsUpdateResult{}, err
	}
	next.Port, err = s.store.NextPort()
	if err != nil {
		return VarsUpdateResult{}, err
	}
	next.CreatedAt = time.Now().UTC()
	next.CreatedBy = strings.TrimSpace(input.CreatedBy)
	next.CommitMessage = input.CommitMessage
	next.Vars = cloneVars(input.Vars)
	result := VarsUpdateResult{ID: next.ID, BaseDeploymentID: current.Deployment.ID, CommitMessage: next.CommitMessage, CreatedBy: next.CreatedBy, CreatedAt: next.CreatedAt}
	op = VarsUpdateOperation{AppID: appID, Key: input.IdempotencyKey, Fingerprint: fingerprint, Status: "pending", Result: result}
	if err := s.store.BeginVarsUpdate(op, next, input.ExpectedDeploymentID); err != nil {
		return VarsUpdateResult{}, err
	}
	if err := s.publishActiveDeployments(appID); err != nil {
		// Record rollback intent before changing traffic, so restart recovery can
		// distinguish a partial rollback from a successfully superseded revision.
		if intentErr := s.store.FinishVarsUpdate(appID, op.Key, "rolling_back"); intentErr != nil {
			return VarsUpdateResult{}, fmt.Errorf("publish vars update: %w; save rollback intent: %v", err, intentErr)
		}
		if rollbackErr := s.rollbackDeployment(appID, traffic, next); rollbackErr != nil {
			return VarsUpdateResult{}, fmt.Errorf("publish vars update: %w; rollback: %v", err, rollbackErr)
		}
		if finishErr := s.store.FinishVarsUpdate(appID, op.Key, "failed"); finishErr != nil {
			return VarsUpdateResult{}, fmt.Errorf("publish vars update: %w; save failed outcome: %v", err, finishErr)
		}
		return VarsUpdateResult{}, fmt.Errorf("publish vars update: %w", err)
	}
	if err := s.store.FinishVarsUpdate(appID, op.Key, "succeeded"); err != nil {
		return VarsUpdateResult{}, fmt.Errorf("vars update activated; retry the same request to recover its result: %w", err)
	}
	return result, nil
}

func (s *Service) publishActiveDeployments(appID string) error {
	s.invalidateRuntimeDeployments(appID)
	active, err := s.activeDeployments()
	if err != nil {
		return err
	}
	return s.writer.Write(active)
}

// recoverVarsRollback completes an interrupted rollback without overwriting a
// subsequent deployment or traffic allocation. If the failed revision is still
// selected explicitly, leave the operation recoverable rather than deleting it.
func (s *Service) recoverVarsRollback(op VarsUpdateOperation) error {
	active, err := s.store.ActiveDeploymentsForApp(op.AppID)
	if err != nil {
		return err
	}
	traffic := activeTrafficForApp(active, op.AppID)
	for _, item := range traffic {
		if item.ID != op.Result.ID {
			continue
		}
		if len(traffic) != 1 || item.TrafficPercent != 100 {
			return fmt.Errorf("cannot finish vars rollback while failed revision participates in split traffic")
		}
		if err := s.store.SetActive(op.AppID, op.Result.BaseDeploymentID); err != nil {
			return err
		}
	}
	records, err := s.store.ListDeployments()
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Deployment.AppID != op.AppID || record.Deployment.ID != op.Result.ID {
			continue
		}
		if err := s.store.DeleteDeployment(record.Deployment.ID); err != nil {
			return err
		}
		s.cleanupDeploymentObject(record.Deployment)
		s.cleanupDeploymentAssets(record.Deployment)
		break
	}
	if err := s.publishActiveDeployments(op.AppID); err != nil {
		return err
	}
	return s.store.FinishVarsUpdate(op.AppID, op.Key, "failed")
}
