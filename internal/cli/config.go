package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/evanw/esbuild/pkg/api"
	"github.com/pelletier/go-toml/v2"
	"github.com/tidwall/jsonc"
)

// Keep native projects preferred so existing build manifests remain authoritative.
func discoverProjectPath() (string, error) {
	for _, name := range []string{projectFilename, "nanoflare.jsonc", "nanoflare.toml", "wrangler.json", "wrangler.jsonc", "wrangler.toml"} {
		_, err := os.Stat(name)
		if err == nil {
			return filepath.Abs(name)
		}
		if !os.IsNotExist(err) {
			return "", err
		}
	}
	return filepath.Abs(projectFilename)
}

func decodeProjectConfig(path string, content []byte) ([]byte, map[string]string, error) {
	if os.Getenv("CLOUDFLARE_ENV") != "" {
		return nil, nil, fmt.Errorf("%s: named Wrangler environments are not supported yet; unset CLOUDFLARE_ENV to use top-level configuration", path)
	}
	if filepath.Ext(path) == ".toml" {
		var value map[string]any
		if err := toml.Unmarshal(content, &value); err != nil {
			return nil, nil, fmt.Errorf("decode %s: %w", path, err)
		}
		var err error
		content, err = json.Marshal(value)
		if err != nil {
			return nil, nil, err
		}
	} else {
		content = jsonc.ToJSON(content)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(content, &raw); err != nil {
		return nil, nil, fmt.Errorf("decode %s: %w", path, err)
	}
	// Reject unknown behavior instead of silently deploying a different application.
	allowed := strings.Fields("$schema name main compatibility_date compatibility_flags vars kv_namespaces d1_databases r2_buckets services assets triggers account_id workers_dev preview_urls env dispatch_namespaces nanoflare files format entrypoint db object_storage_buckets object_storage_bucket worker_id dns auth secrets vite api_url hostname")
	supported := make(map[string]bool)
	for _, key := range allowed {
		supported[key] = true
	}
	var unsupported []string
	for key := range raw {
		if !supported[key] {
			unsupported = append(unsupported, key)
		}
	}
	sort.Strings(unsupported)
	if len(unsupported) > 0 {
		return nil, nil, fmt.Errorf("%s: unsupported Wrangler settings: %s", path, strings.Join(unsupported, ", "))
	}
	// Validate nested behavior too (for example service entrypoints and remote
	// bindings cannot be represented by the current Nanoflare runtime).
	for key, fields := range map[string]string{
		"kv_namespaces":       "binding id preview_id",
		"d1_databases":        "binding database_id database_name preview_database_id migrations_dir migrations_table migrations_pattern",
		"r2_buckets":          "binding bucket_name preview_bucket_name jurisdiction",
		"services":            "binding service worker_id execution",
		"dispatch_namespaces": "binding namespace namespace_id",
		"assets":              "binding directory html_handling not_found_handling run_worker_first",
		"triggers":            "crons",
	} {
		value, exists := raw[key]
		if !exists {
			continue
		}
		var objects []map[string]json.RawMessage
		if key == "assets" || key == "triggers" {
			var object map[string]json.RawMessage
			if err := json.Unmarshal(value, &object); err != nil {
				return nil, nil, fmt.Errorf("%s: %s: %w", path, key, err)
			}
			objects = append(objects, object)
		} else if err := json.Unmarshal(value, &objects); err != nil {
			return nil, nil, fmt.Errorf("%s: %s: %w", path, key, err)
		}
		for _, object := range objects {
			for field := range object {
				if !strings.Contains(" "+fields+" ", " "+field+" ") {
					return nil, nil, fmt.Errorf("%s: unsupported Wrangler setting %s.%s", path, key, field)
				}
			}
		}
	}

	// Normalize source configuration into the internal deployment representation.
	// This path is identical for both product names and all serialization formats.
	legacy := raw["files"] != nil || raw["db"] != nil || raw["object_storage_buckets"] != nil || raw["object_storage_bucket"] != nil
	var extension struct {
		WorkerID    string            `json:"worker_id"`
		DNS         json.RawMessage   `json:"dns"`
		Auth        json.RawMessage   `json:"auth"`
		Secrets     json.RawMessage   `json:"secrets"`
		ResourceIDs map[string]string `json:"resource_ids"`
	}
	if value := raw["nanoflare"]; value != nil {
		decoder := json.NewDecoder(strings.NewReader(string(value)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&extension); err != nil {
			return nil, nil, fmt.Errorf("%s: nanoflare: %w", path, err)
		}
		for key, value := range map[string]json.RawMessage{"dns": extension.DNS, "auth": extension.Auth, "secrets": extension.Secrets} {
			if value != nil {
				if raw[key] != nil {
					return nil, nil, fmt.Errorf("%s: both %s and nanoflare.%s are configured", path, key, key)
				}
				raw[key] = value
			}
		}
		if extension.WorkerID != "" {
			if raw["worker_id"] != nil {
				return nil, nil, fmt.Errorf("%s: both worker_id and nanoflare.worker_id are configured", path)
			}
			raw["worker_id"], _ = json.Marshal(extension.WorkerID)
		}
	}
	for canonical, alias := range map[string]string{"d1_databases": "db", "r2_buckets": "object_storage_buckets"} {
		if value := raw[canonical]; value != nil {
			if raw[alias] != nil || (canonical == "r2_buckets" && raw["object_storage_bucket"] != nil) {
				return nil, nil, fmt.Errorf("%s: cannot combine %s with its legacy alias", path, canonical)
			}
			var bindings []map[string]json.RawMessage
			if err := json.Unmarshal(value, &bindings); err != nil {
				return nil, nil, fmt.Errorf("%s: %w", path, err)
			}
			for _, binding := range bindings {
				delete(binding, "database_id") // Cloudflare IDs cannot identify Nanoflare resources.
			}
			raw[alias], _ = json.Marshal(bindings)
		}
	}
	if !legacy {
		var bindings []map[string]json.RawMessage
		if value := raw["kv_namespaces"]; value != nil {
			if err := json.Unmarshal(value, &bindings); err != nil {
				return nil, nil, err
			}
			for _, binding := range bindings {
				delete(binding, "id")
				delete(binding, "preview_id")
			}
			raw["kv_namespaces"], _ = json.Marshal(bindings)
		}
	}
	var dispatch []struct {
		Binding     string `json:"binding"`
		Namespace   string `json:"namespace"`
		NamespaceID string `json:"namespace_id"`
	}
	if value := raw["dispatch_namespaces"]; value != nil {
		if err := json.Unmarshal(value, &dispatch); err != nil {
			return nil, nil, err
		}
	}
	names := make(map[string]string)
	for _, binding := range dispatch {
		if strings.TrimSpace(binding.Binding) == "" || (strings.TrimSpace(binding.Namespace) == "" && strings.TrimSpace(binding.NamespaceID) == "") {
			return nil, nil, fmt.Errorf("%s: dispatch_namespaces requires binding and namespace", path)
		}
		if binding.Namespace != "" && binding.NamespaceID != "" {
			return nil, nil, fmt.Errorf("%s: dispatch binding %s cannot combine namespace and namespace_id", path, binding.Binding)
		}
		if binding.Namespace != "" {
			names[binding.Binding] = binding.Namespace
		}
	}
	normalized, err := json.Marshal(raw)
	if err != nil {
		return nil, nil, err
	}
	var project Project
	if err := json.Unmarshal(normalized, &project); err != nil {
		return nil, nil, err
	}
	var alias projectAlias
	if err := json.Unmarshal(normalized, &alias); err != nil {
		return nil, nil, err
	}
	if len(project.ObjectStorageBuckets) == 0 || hasEmptyObjectStorageBucketIDs(project.ObjectStorageBuckets) {
		legacy := alias.ObjectStorageBuckets
		if len(legacy) == 0 {
			legacy = alias.ObjectStorageBucketLegacy
		}
		if len(legacy) > 0 {
			project.ObjectStorageBuckets = project.ObjectStorageBuckets[:0]
		}
		for _, binding := range legacy {
			bucketID := strings.TrimSpace(binding.BucketID)
			if bucketID == "" {
				bucketID = strings.TrimSpace(binding.ID)
			}
			project.ObjectStorageBuckets = append(project.ObjectStorageBuckets, nanoflare.ObjectStorageBucketBinding{
				Binding:  binding.Binding,
				BucketID: bucketID,
			})
		}
	}
	knownResources := make(map[string]bool)
	for _, binding := range project.KVNamespaces {
		knownResources[binding.Binding] = true
	}
	for _, binding := range project.Databases {
		knownResources[binding.Binding] = true
	}
	for _, binding := range project.ObjectStorageBuckets {
		knownResources[binding.Binding] = true
	}
	for _, binding := range project.DispatchNamespaces {
		knownResources[binding.Binding] = true
	}
	for _, binding := range project.Services {
		knownResources[binding.Binding] = true
	}
	for binding, id := range extension.ResourceIDs {
		if !knownResources[binding] || strings.TrimSpace(id) == "" {
			return nil, nil, fmt.Errorf("%s: nanoflare.resource_ids.%s must name a configured resource binding and contain a nonempty ID", path, binding)
		}
	}
	for i := range project.KVNamespaces {
		if id := extension.ResourceIDs[project.KVNamespaces[i].Binding]; id != "" {
			project.KVNamespaces[i].ID = id
		}
	}
	for i := range project.Databases {
		if id := extension.ResourceIDs[project.Databases[i].Binding]; id != "" {
			project.Databases[i].DatabaseID = id
		}
	}
	for i := range project.ObjectStorageBuckets {
		if id := extension.ResourceIDs[project.ObjectStorageBuckets[i].Binding]; id != "" {
			project.ObjectStorageBuckets[i].BucketID = id
		}
	}
	for i := range project.Services {
		if id := extension.ResourceIDs[project.Services[i].Binding]; id != "" {
			project.Services[i].WorkerID = id
		}
	}
	for i := range project.DispatchNamespaces {
		if id := extension.ResourceIDs[project.DispatchNamespaces[i].Binding]; id != "" {
			project.DispatchNamespaces[i].NamespaceID = id
			delete(names, project.DispatchNamespaces[i].Binding)
		}
	}
	// Preserve legacy alias handling below while injecting normalized bindings.
	for key, value := range map[string]any{"kv_namespaces": project.KVNamespaces, "db": project.Databases, "services": project.Services} {
		raw[key], _ = json.Marshal(value)
	}
	if raw["r2_buckets"] != nil || raw["object_storage_buckets"] != nil || raw["object_storage_bucket"] != nil {
		raw["object_storage_buckets"], _ = json.Marshal(project.ObjectStorageBuckets)
	}
	raw["dispatch_namespaces"], _ = json.Marshal(project.DispatchNamespaces)
	normalized, err = json.Marshal(raw)
	return normalized, names, err
}

func bundleSourceProject(path string, project Project) (Project, error) {
	result := api.Build(api.BuildOptions{
		AbsWorkingDir: filepath.Dir(path), EntryPoints: []string{project.Main},
		Bundle: true, Write: false, Format: api.FormatESModule, Platform: api.PlatformNeutral,
		Target: api.ES2022, Outfile: "worker.mjs", External: []string{"cloudflare:*", "node:*"},
	})
	if len(result.Errors) > 0 {
		return Project{}, fmt.Errorf("bundle %s: %s", path, strings.Join(api.FormatMessages(result.Errors, api.FormatMessagesOptions{Kind: api.ErrorMessage}), "\n"))
	}
	if len(result.OutputFiles) != 1 {
		return Project{}, fmt.Errorf("bundle %s: expected one Worker module", path)
	}
	project.Format = "modules"
	project.Main = "worker.mjs"
	project.Files = []string{project.Main}
	project.bundledFiles = []nanoflare.WorkerFile{{Path: project.Main, Content: string(result.OutputFiles[0].Contents)}}
	return project, nil
}

func projectWorkerFiles(project Project) ([]nanoflare.WorkerFile, error) {
	if project.bundledFiles != nil {
		return project.bundledFiles, nil
	}
	return loadWorkerFiles(project.Files)
}

// Legacy entrypoint migration preserves source metadata and serialization syntax.
func migrateProjectEntrypoint(path string, content []byte, main string) error {
	var source map[string]any
	var err error
	if filepath.Ext(path) == ".toml" {
		err = toml.Unmarshal(content, &source)
	} else {
		err = json.Unmarshal(jsonc.ToJSON(content), &source)
	}
	if err != nil {
		return err
	}
	delete(source, "entrypoint")
	source["main"] = main
	if filepath.Ext(path) == ".toml" {
		content, err = toml.Marshal(source)
	} else {
		content, err = json.MarshalIndent(source, "", "  ")
		content = append(content, '\n')
	}
	if err != nil {
		return err
	}
	return os.WriteFile(path, content, 0644)
}
