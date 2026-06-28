package platform

import "time"

type App struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Hostname     string    `json:"hostname"`
	RuntimeToken string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

type Deployment struct {
	ID                string       `json:"id"`
	AppID             string       `json:"app_id"`
	Files             []WorkerFile `json:"files"`
	Assets            []AssetFile  `json:"assets,omitempty"`
	Entrypoint        string       `json:"entrypoint"`
	Format            string       `json:"format"`
	CompatibilityDate string       `json:"compatibility_date"`
	AssetConfig       AssetConfig  `json:"asset_config,omitempty"`
	BundleSize        int64        `json:"-"`
	ObjectKey         string       `json:"-"`
	Port              int          `json:"port"`
	CreatedAt         time.Time    `json:"created_at"`
}

type ActiveDeployment struct {
	App        App        `json:"app"`
	Deployment Deployment `json:"deployment"`
}

type DeploymentRecord struct {
	App        App
	Deployment Deployment
	Active     bool
}

type CreateAppInput struct {
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
}

type DeployInput struct {
	Files             []WorkerFile `json:"files"`
	Assets            []AssetFile  `json:"assets,omitempty"`
	Entrypoint        string       `json:"entrypoint,omitempty"`
	Format            string       `json:"format,omitempty"`
	CompatibilityDate string       `json:"compatibility_date"`
	AssetConfig       AssetConfig  `json:"asset_config,omitempty"`
}

type WorkerDeployment struct {
	ID                string      `json:"id"`
	Entrypoint        string      `json:"entrypoint"`
	Format            string      `json:"format"`
	BundleSize        int64       `json:"bundle_size"`
	AssetCount        int         `json:"asset_count,omitempty"`
	CompatibilityDate string      `json:"compatibility_date"`
	AssetConfig       AssetConfig `json:"asset_config,omitempty"`
	Port              int         `json:"port"`
	CreatedAt         time.Time   `json:"created_at"`
}

type WorkerDetail struct {
	App        App               `json:"app"`
	Deployment *WorkerDeployment `json:"deployment,omitempty"`
}

type ConsoleDeployment struct {
	ID                string    `json:"id"`
	AppID             string    `json:"app_id"`
	AppName           string    `json:"app_name"`
	Hostname          string    `json:"hostname"`
	Entrypoint        string    `json:"entrypoint"`
	Format            string    `json:"format"`
	BundleSize        int64     `json:"bundle_size"`
	AssetCount        int       `json:"asset_count,omitempty"`
	CompatibilityDate string    `json:"compatibility_date"`
	State             string    `json:"state"`
	CreatedAt         time.Time `json:"created_at"`
}

type WorkerFile struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Content string `json:"content"`
}

type AssetFile struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type,omitempty"`
	ObjectKey   string `json:"object_key,omitempty"`
	Data        []byte `json:"data,omitempty"`
}

type AssetConfig struct {
	HTMLHandling     string `json:"html_handling,omitempty"`
	NotFoundHandling string `json:"not_found_handling,omitempty"`
	RunWorkerFirst   bool   `json:"run_worker_first,omitempty"`
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
