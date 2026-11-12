package nanoflare

import (
	"errors"
	"strings"
	"testing"
)

func TestDeploymentLimitsSeparateCodeAssetsAndTotal(t *testing.T) {
	limits := DeploymentLimits{WorkerBytes: 10, AssetBytes: 20, RequestBytes: 30}
	input := DeployInput{Files: []WorkerFile{{Content: strings.Repeat("x", 10)}}, Assets: []AssetFile{{Path: "a", Data: make([]byte, 20)}}}
	if err := limits.Validate(input); err != nil {
		t.Fatal(err)
	}
	input.Assets = append(input.Assets, AssetFile{Path: "b", Data: []byte{1}})
	if err := limits.Validate(input); !errors.Is(err, ErrDeploymentTooLarge) {
		t.Fatal(err)
	}
	input.Assets = nil
	input.Files = append(input.Files, WorkerFile{Wasm: []byte{1}})
	if err := limits.Validate(input); !errors.Is(err, ErrDeploymentTooLarge) {
		t.Fatal(err)
	}
	input.Files = nil
	input.Assets = []AssetFile{{Path: "a", Data: make([]byte, 21)}}
	if err := limits.Validate(input); !errors.Is(err, ErrDeploymentTooLarge) {
		t.Fatal(err)
	}
}

func TestDefaultDeploymentLimitsAndRuntimeCapacity(t *testing.T) {
	limits := DefaultDeploymentLimits()
	if limits.WorkerBytes != 30<<20 || limits.AssetBytes != 50<<20 || limits.RequestBytes != 250<<20 {
		t.Fatal(limits)
	}
	service := NewService(NewStore(), nil)
	if err := service.SetDeploymentLimits(limits); err != nil {
		t.Fatal(err)
	}
	if err := service.SetDeploymentLimits(DeploymentLimits{WorkerBytes: 100 << 20, AssetBytes: 50 << 20, RequestBytes: 250 << 20}); err == nil {
		t.Fatal("accepted limits that exceed runner capacity")
	}
}
