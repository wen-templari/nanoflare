package nanoflare

import (
	"errors"
	"fmt"
)

// RuntimeSnapshotMaxBytes allows for JSON escaping of code and base64 encoding
// of binary modules and inline assets on internal runner connections.
const RuntimeSnapshotMaxBytes = 512 << 20

var ErrDeploymentTooLarge = errors.New("deployment exceeds upload limit")

type DeploymentLimits struct {
	WorkerBytes  int64 `json:"worker_bytes"`
	AssetBytes   int64 `json:"asset_bytes"`
	RequestBytes int64 `json:"request_bytes"`
}

func DefaultDeploymentLimits() DeploymentLimits {
	return DeploymentLimits{WorkerBytes: 30 << 20, AssetBytes: 50 << 20, RequestBytes: 250 << 20}
}

// SetDeploymentLimits must be called before serving requests. The bound also
// ensures accepted deployments fit the internal runner snapshot protocol.
func (s *Service) SetDeploymentLimits(limits DeploymentLimits) error {
	if limits.WorkerBytes <= 0 || limits.AssetBytes <= 0 || limits.RequestBytes <= 0 ||
		limits.WorkerBytes > limits.RequestBytes || limits.AssetBytes > limits.RequestBytes ||
		limits.RequestBytes > RuntimeSnapshotMaxBytes ||
		6*limits.WorkerBytes+(limits.RequestBytes-limits.WorkerBytes)*4/3+(16<<20) > RuntimeSnapshotMaxBytes {
		return errors.New("invalid deployment limits or limits exceed internal runtime snapshot capacity")
	}
	s.deploymentLimits = limits
	return nil
}

func (s *Service) DeploymentLimits() DeploymentLimits { return s.deploymentLimits }

func (limits DeploymentLimits) Validate(input DeployInput) error {
	var code, total int64
	for _, file := range input.Files {
		size := int64(len(file.Content) + len(file.Wasm))
		code += size
		total += size
	}
	if code > limits.WorkerBytes {
		return fmt.Errorf("%w: worker code exceeds %d bytes", ErrDeploymentTooLarge, limits.WorkerBytes)
	}
	for _, asset := range input.Assets {
		size := int64(len(asset.Data))
		if size > limits.AssetBytes {
			return fmt.Errorf("%w: asset %q exceeds %d bytes", ErrDeploymentTooLarge, asset.Path, limits.AssetBytes)
		}
		total += size
	}
	if total > limits.RequestBytes {
		return fmt.Errorf("%w: deployment files exceed %d bytes", ErrDeploymentTooLarge, limits.RequestBytes)
	}
	return nil
}
