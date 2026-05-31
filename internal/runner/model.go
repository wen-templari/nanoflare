package runner

import "github.com/clas/platform/internal/platform"

type PrepareRequest struct {
	Deployments []platform.ActiveDeployment `json:"deployments"`
}

type PrepareResponse struct {
	Generation  string                      `json:"generation"`
	Deployments []platform.ActiveDeployment `json:"deployments"`
}
