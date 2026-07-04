package runner

import "github.com/clas/nanoflare/internal/nanoflare"

type PrepareRequest struct {
	Deployments []runtimeDeployment `json:"deployments"`
}

type PrepareResponse struct {
	Generation  string                       `json:"generation"`
	Deployments []nanoflare.ActiveDeployment `json:"deployments"`
}

type runtimeDeployment struct {
	App          nanoflare.App        `json:"app"`
	Deployment   nanoflare.Deployment `json:"deployment"`
	RuntimeToken string               `json:"runtime_token"`
}

func prepareRequest(deployments []nanoflare.ActiveDeployment) PrepareRequest {
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

func (r PrepareRequest) activeDeployments() []nanoflare.ActiveDeployment {
	items := make([]nanoflare.ActiveDeployment, len(r.Deployments))
	for i, deployment := range r.Deployments {
		deployment.App.RuntimeToken = deployment.RuntimeToken
		items[i] = nanoflare.ActiveDeployment{App: deployment.App, Deployment: deployment.Deployment}
	}
	return items
}
