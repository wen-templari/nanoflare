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

type WorkerDeployment struct {
	ID                string    `json:"id"`
	BundlePath        string    `json:"bundle_path"`
	BundleSize        int64     `json:"bundle_size"`
	CompatibilityDate string    `json:"compatibility_date"`
	Port              int       `json:"port"`
	CreatedAt         time.Time `json:"created_at"`
}

type WorkerDetail struct {
	App        App               `json:"app"`
	Deployment *WorkerDeployment `json:"deployment,omitempty"`
}

type WorkerFile struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Content string `json:"content"`
}

type WorkerOutputLine struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

type WorkerStatusCode struct {
	Code  string  `json:"code"`
	Value float64 `json:"value"`
}

type WorkerTraffic struct {
	Available         bool               `json:"available"`
	RequestsPerSecond float64            `json:"requests_per_second"`
	P95Latency        float64            `json:"p95_latency"`
	ErrorRate         float64            `json:"error_rate"`
	Traffic           []float64          `json:"traffic"`
	StatusCodes       []WorkerStatusCode `json:"status_codes"`
}
