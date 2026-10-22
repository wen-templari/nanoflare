package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func (p *Postgres) migrateManagement(ctx context.Context) error {
	_, err := p.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS worker_management (
 worker_id text PRIMARY KEY REFERENCES workers(id) ON DELETE CASCADE,
 revision bigint NOT NULL CHECK(revision>0),
 deployment_id text NOT NULL REFERENCES deployment_artifacts(deployment_id),
 payload jsonb NOT NULL);`)
	return err
}
func (p *Postgres) GetScriptManagementState(worker string) (nanoflare.ScriptManagementState, bool, error) {
	var state nanoflare.ScriptManagementState
	var data []byte
	err := p.db.QueryRow(`SELECT m.payload FROM workers w LEFT JOIN worker_management m ON m.worker_id=w.id WHERE w.id=$1`, worker).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return state, false, nanoflare.ErrAppNotFound
	}
	if err != nil {
		return state, false, err
	}
	if len(data) == 0 {
		return state, false, nil
	}
	err = json.Unmarshal(data, &state)
	return state, err == nil, err
}
func (p *Postgres) CommitScriptManagementState(state nanoflare.ScriptManagementState, expected int64) error {
	if expected < 0 || state.Revision != expected+1 {
		return nanoflare.ErrManagementConflict
	}
	// The INSERT validates both ownership and the artifact's Worker before its
	// optimistic update. No metadata can point at a foreign retained artifact.
	result, err := p.db.Exec(`INSERT INTO worker_management(worker_id,revision,deployment_id,payload)
 SELECT w.id,$2,a.deployment_id,$3 FROM workers w JOIN deployment_artifacts a ON a.worker_id=w.id
 WHERE w.id=$1 AND w.org_id=$4 AND a.deployment_id=$5
 AND ($6=0 OR EXISTS(SELECT 1 FROM worker_management prior WHERE prior.worker_id=w.id))
 ON CONFLICT(worker_id) DO UPDATE SET revision=EXCLUDED.revision,deployment_id=EXCLUDED.deployment_id,payload=EXCLUDED.payload
 WHERE worker_management.revision=$6`, state.WorkerID, state.Revision, mustJSON(state), state.OrgID, state.DeploymentID, expected)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return nanoflare.ErrManagementConflict
	}
	return nil
}
