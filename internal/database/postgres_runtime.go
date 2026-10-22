package database

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func (p *Postgres) GetRuntimeDeployment(appID, deploymentID string) (nanoflare.Deployment, error) {
	var d nanoflare.Deployment
	var kv, db, objects, assets, assetConfig []byte
	err := p.db.QueryRow(`SELECT id,worker_id,kv_namespaces,db,object_storage_bucket,assets,asset_config FROM deployments WHERE worker_id=$1 AND id=$2`, appID, deploymentID).Scan(&d.ID, &d.AppID, &kv, &db, &objects, &assets, &assetConfig)
	if errors.Is(err, sql.ErrNoRows) {
		return d, nanoflare.ErrAppNotFound
	}
	if err != nil {
		return d, err
	}
	for _, part := range []struct {
		data []byte
		out  any
	}{{kv, &d.KVNamespaces}, {db, &d.Databases}, {objects, &d.ObjectStorageBuckets}, {assets, &d.Assets}, {assetConfig, &d.AssetConfig}} {
		if err = json.Unmarshal(part.data, part.out); err != nil {
			return d, err
		}
	}
	return d, nil
}
