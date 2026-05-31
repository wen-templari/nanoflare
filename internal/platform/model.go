package platform

import "time"

type App struct {
	ID        string    `json:"id"`
	Hostname  string    `json:"hostname"`
	CreatedAt time.Time `json:"created_at"`
}

type Deployment struct {
	ID                string    `json:"id"`
	AppID             string    `json:"app_id"`
	BundlePath        string    `json:"bundle_path"`
	CompatibilityDate string    `json:"compatibility_date"`
	Port              int       `json:"port"`
	CapabilityToken   string    `json:"capability_token"`
	CreatedAt         time.Time `json:"created_at"`
}

type ActiveDeployment struct {
	App        App        `json:"app"`
	Deployment Deployment `json:"deployment"`
}

type CreateAppInput struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
}

type DeployInput struct {
	BundlePath        string `json:"bundle_path"`
	CompatibilityDate string `json:"compatibility_date"`
}
