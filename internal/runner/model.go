package runner

import "github.com/clas/nanoflare/internal/nanoflare"

type PrepareRequest struct {
	ArtifactProtocol int                 `json:"artifact_protocol"`
	Deployments      []runtimeDeployment `json:"deployments"`
}

type PrepareResponse struct {
	Generation  string                       `json:"generation"`
	Deployments []nanoflare.ActiveDeployment `json:"deployments"`
}

type runtimeDeployment struct {
	App            nanoflare.App        `json:"app"`
	Deployment     nanoflare.Deployment `json:"deployment"`
	ObjectKey      string               `json:"object_key,omitempty"`
	SecretValues   map[string]string    `json:"secret_values,omitempty"`
	TrafficPercent int                  `json:"traffic_percent"`
	RuntimeToken   string               `json:"runtime_token"`
}

func prepareRequest(deployments []nanoflare.ActiveDeployment) PrepareRequest {
	items := make([]runtimeDeployment, len(deployments))
	for i, deployment := range deployments {
		items[i] = runtimeDeployment{
			App:            deployment.App,
			Deployment:     deployment.Deployment,
			RuntimeToken:   deployment.App.RuntimeToken,
			SecretValues:   deployment.App.SecretValues,
			ObjectKey:      deployment.Deployment.ObjectKey,
			TrafficPercent: deployment.TrafficPercent,
		}
	}
	return PrepareRequest{Deployments: items, ArtifactProtocol: 1}
}

func (r PrepareRequest) activeDeployments() []nanoflare.ActiveDeployment {
	items := make([]nanoflare.ActiveDeployment, len(r.Deployments))
	for i, deployment := range r.Deployments {
		deployment.Deployment.ObjectKey = deployment.ObjectKey
		deployment.App.RuntimeToken = deployment.RuntimeToken
		deployment.App.SecretValues = deployment.SecretValues
		items[i] = nanoflare.ActiveDeployment{App: deployment.App, Deployment: deployment.Deployment, TrafficPercent: deployment.TrafficPercent}
	}
	return items
}
