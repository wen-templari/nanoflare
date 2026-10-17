package database

import (
	"database/sql"
	"encoding/json"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func advancePublicationTx(tx *sql.Tx, appID string) error {
	_, err := tx.Exec(`INSERT INTO worker_publications (worker_id, generation, traffic)
 SELECT $1, 1, COALESCE(jsonb_agg(jsonb_build_object('id', id, 'traffic_percent', traffic_percent)) FILTER (WHERE traffic_percent > 0), '[]'::jsonb) FROM deployments WHERE worker_id = $1
 ON CONFLICT (worker_id) DO UPDATE SET generation = worker_publications.generation + 1, traffic = EXCLUDED.traffic`, appID)
	return err
}

func (p *Postgres) PublicationStates() ([]nanoflare.WorkerPublication, error) {
	rows, err := p.db.Query(`SELECT worker_id, generation, published_generation, traffic FROM worker_publications ORDER BY worker_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var states []nanoflare.WorkerPublication
	for rows.Next() {
		var state nanoflare.WorkerPublication
		var traffic []byte
		if err := rows.Scan(&state.AppID, &state.Generation, &state.PublishedGeneration, &traffic); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(traffic, &state.Traffic); err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

func (p *Postgres) AcknowledgePublication(appID string, generation uint64) error {
	result, err := p.db.Exec(`UPDATE worker_publications SET published_generation = GREATEST(published_generation, $2) WHERE worker_id = $1 AND generation >= $2`, appID, generation)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return nanoflare.ErrDeploymentConflict
	}
	return err
}
