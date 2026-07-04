package nanoflare

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"
)

type App struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Hostname     string     `json:"hostname"`
	Auth         AuthConfig `json:"auth,omitempty"`
	RuntimeToken string     `json:"-"`
	CreatedAt    time.Time  `json:"created_at"`
}

type AuthConfig struct {
	ProtectedRoutes []string `json:"protected_routes,omitempty"`
}

type Deployment struct {
	ID                string       `json:"id"`
	AppID             string       `json:"app_id"`
	Files             []WorkerFile `json:"files"`
	Assets            []AssetFile  `json:"assets,omitempty"`
	Entrypoint        string       `json:"entrypoint"`
	Format            string       `json:"format"`
	CompatibilityDate string       `json:"compatibility_date"`
	KVNamespaces      []KVBinding  `json:"kv_namespaces,omitempty"`
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
	Name     string     `json:"name"`
	Hostname string     `json:"hostname"`
	Auth     AuthConfig `json:"auth,omitempty"`
}

type CreateKVNamespaceInput struct {
	Name string `json:"name"`
}

type UpdateAppInput struct {
	Auth *AuthConfig `json:"auth,omitempty"`
}

type DeployInput struct {
	Files             []WorkerFile `json:"files"`
	Assets            []AssetFile  `json:"assets,omitempty"`
	Entrypoint        string       `json:"entrypoint,omitempty"`
	Format            string       `json:"format,omitempty"`
	CompatibilityDate string       `json:"compatibility_date"`
	KVNamespaces      []KVBinding  `json:"kv_namespaces,omitempty"`
	AssetConfig       AssetConfig  `json:"asset_config,omitempty"`
}

type WorkerDeployment struct {
	ID                string      `json:"id"`
	Entrypoint        string      `json:"entrypoint"`
	Format            string      `json:"format"`
	BundleSize        int64       `json:"bundle_size"`
	AssetCount        int         `json:"asset_count,omitempty"`
	CompatibilityDate string      `json:"compatibility_date"`
	KVNamespaces      []KVBinding `json:"kv_namespaces,omitempty"`
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
	Binding          string         `json:"binding,omitempty"`
	HTMLHandling     string         `json:"html_handling,omitempty"`
	NotFoundHandling string         `json:"not_found_handling,omitempty"`
	RunWorkerFirst   RunWorkerFirst `json:"run_worker_first,omitempty"`
}

type KVBinding struct {
	Binding   string `json:"binding"`
	ID        string `json:"id"`
	PreviewID string `json:"preview_id,omitempty"`
}

type KVNamespace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type RunWorkerFirst []string

const runWorkerFirstAlways = "\x00always"

func (r RunWorkerFirst) Always() bool {
	return len(r) == 1 && r[0] == runWorkerFirstAlways
}

func (r RunWorkerFirst) Routes() []string {
	if r.Always() {
		return nil
	}
	return []string(r)
}

func (r RunWorkerFirst) MarshalJSON() ([]byte, error) {
	if r.Always() {
		return []byte("true"), nil
	}
	return json.Marshal([]string(r))
}

func (r *RunWorkerFirst) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	switch {
	case bytes.Equal(data, []byte("true")):
		*r = RunWorkerFirst{runWorkerFirstAlways}
		return nil
	case bytes.Equal(data, []byte("false")), bytes.Equal(data, []byte("null")):
		*r = nil
		return nil
	case len(data) > 0 && data[0] == '[':
		var routes []string
		if err := json.Unmarshal(data, &routes); err != nil {
			return err
		}
		*r = RunWorkerFirst(routes)
		return nil
	default:
		return errors.New(`run_worker_first must be true, false, or an array of route patterns`)
	}
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

type WorkerKVKey struct {
	Key  string `json:"key"`
	Size int64  `json:"size"`
}

type ObjectHTTPMetadata struct {
	ContentType string `json:"contentType,omitempty"`
}

type ObjectInfo struct {
	Key          string             `json:"key"`
	Size         int64              `json:"size"`
	ETag         string             `json:"etag,omitempty"`
	HTTPETag     string             `json:"httpEtag,omitempty"`
	Uploaded     time.Time          `json:"uploaded"`
	HTTPMetadata ObjectHTTPMetadata `json:"httpMetadata,omitempty"`
}

type ObjectBody struct {
	ObjectInfo
	Body []byte `json:"-"`
}
