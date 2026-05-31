package runner

import "github.com/clas/platform/internal/platform"

type PrepareRequest struct {
	Deployments []runtimeDeployment `json:"deployments"`
}

type PrepareResponse struct {
	Generation  string                      `json:"generation"`
	Deployments []platform.ActiveDeployment `json:"deployments"`
}

type runtimeDeployment struct {
	App          platform.App        `json:"app"`
	Deployment   platform.Deployment `json:"deployment"`
	RuntimeToken string              `json:"runtime_token"`
}

func prepareRequest(deployments []platform.ActiveDeployment) PrepareRequest {
	items := make([]runtimeDeployment, len(deployments))
	for i, deployment := range deployments {
		items[i] = runtimeDeployment{
			App:          deployment.App,
			Deployment:   deployment.Deployment,
			RuntimeToken: deployment.App.RuntimeToken,
		}
	}
	return PrepareRequest{Deployments: items}
}

func (r PrepareRequest) activeDeployments() []platform.ActiveDeployment {
	items := make([]platform.ActiveDeployment, len(r.Deployments))
	for i, deployment := range r.Deployments {
		deployment.App.RuntimeToken = deployment.RuntimeToken
		items[i] = platform.ActiveDeployment{App: deployment.App, Deployment: deployment.Deployment}
	}
	return items
}
