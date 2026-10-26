package cli

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func (r *Runner) reuseProjectBindings(baseURL, workerID string, project *Project) error {
	targets := map[string]*string{}
	for i := range project.KVNamespaces {
		b := &project.KVNamespaces[i]
		if b.ID == "" {
			targets["kv:"+b.Binding] = &b.ID
		}
	}
	for i := range project.Databases {
		b := &project.Databases[i]
		if b.DatabaseID == "" {
			targets["db:"+b.Binding] = &b.DatabaseID
		}
	}
	for i := range project.ObjectStorageBuckets {
		b := &project.ObjectStorageBuckets[i]
		if b.BucketID == "" && project.objectStorageBucketNames[b.Binding] == "" {
			targets["r2:"+b.Binding] = &b.BucketID
		}
	}
	if len(targets) == 0 {
		return nil
	}
	var active []nanoflare.WorkerDeployment
	if err := r.request(http.MethodGet, baseURL+"/v1/workers/"+url.PathEscape(workerID)+"/active-bindings", nil, &active); err != nil {
		return fmt.Errorf("read active resource bindings: %w", err)
	}
	traffic := make([]nanoflare.DeploymentTraffic, 0, len(active))
	for _, item := range active {
		if item.TrafficPercent > 0 {
			traffic = append(traffic, nanoflare.DeploymentTraffic{ID: item.ID, TrafficPercent: item.TrafficPercent})
		}
	}
	project.expectedActiveTraffic = &traffic
	ids := map[string]string{}
	for _, deployment := range active {
		if deployment.TrafficPercent <= 0 {
			continue
		}
		bindings := map[string]string{}
		for _, b := range deployment.KVNamespaces {
			bindings["kv:"+b.Binding] = b.ID
		}
		for _, b := range deployment.Databases {
			bindings["db:"+b.Binding] = b.DatabaseID
		}
		for _, b := range deployment.ObjectStorageBuckets {
			bindings["r2:"+b.Binding] = b.BucketID
		}
		for key, id := range bindings {
			if targets[key] == nil {
				continue
			}
			if id == "" {
				return fmt.Errorf("active binding %s has no resource ID; configure an explicit Nanoflare resource ID", key)
			}
			if old, ok := ids[key]; ok && old != id {
				return fmt.Errorf("active deployments disagree on binding %s; configure an explicit Nanoflare resource ID", key)
			}
			ids[key] = id
		}
	}
	// Validate every reused ID before mutating the project or provisioning anything.
	for key, id := range ids {
		var resourcePath string
		switch key[:3] {
		case "kv:":
			resourcePath = "/v1/kv/namespaces/"
		case "db:":
			resourcePath = "/v1/db/"
		default:
			resourcePath = "/v1/object-storage-buckets/"
		}
		var err error
		if resourcePath == "/v1/db/" {
			var databases []nanoflare.Database
			err = r.request(http.MethodGet, baseURL+"/v1/db", nil, &databases)
			if err == nil {
				found := false
				for _, database := range databases {
					if database.ID == id {
						found = true
						break
					}
				}
				if !found {
					err = fmt.Errorf("database is missing or inaccessible")
				}
			}
		} else {
			err = r.request(http.MethodGet, baseURL+resourcePath+url.PathEscape(id), nil, nil)
		}
		if err != nil {
			return fmt.Errorf("reuse binding %s (%s): %w", key, id, err)
		}
	}
	for key, id := range ids {
		*targets[key] = id
	}
	return nil
}
