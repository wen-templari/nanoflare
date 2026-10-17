package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

// Routing publication consumes all active deployments. Use a database-wide
// advisory lock for the same critical section as Service.configMu, including
// runtime publication and rollback, across processes using this database.
func (p *Postgres) LockDeploymentUpdates() (func(), error) {
	var conn *sql.Conn
	const lockID int64 = 0x4e414e4f56415253
	for {
		candidate, err := p.db.Conn(context.Background())
		if err != nil {
			return nil, err
		}
		var acquired bool
		err = candidate.QueryRowContext(context.Background(), `SELECT pg_try_advisory_lock($1)`, lockID).Scan(&acquired)
		if err != nil {
			candidate.Close()
			return nil, err
		}
		if acquired {
			conn = candidate
			break
		}
		// A waiter must not hold a pooled connection while the publisher needs
		// that pool to read/write deployment state.
		_ = candidate.Close()
		time.Sleep(20 * time.Millisecond)
	}
	return func() {
		if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockID); err != nil {
			// Do not return a potentially locked session to the connection pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}, nil
}

func (p *Postgres) VarsUpdate(appID, key string) (nanoflare.VarsUpdateOperation, bool, error) {
	op := nanoflare.VarsUpdateOperation{AppID: appID, Key: key}
	var result []byte
	err := p.db.QueryRow(`SELECT fingerprint, status, result FROM worker_vars_updates WHERE worker_id = $1 AND idempotency_key = $2`, appID, key).Scan(&op.Fingerprint, &op.Status, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return op, false, nil
	}
	if err != nil {
		return op, false, err
	}
	err = json.Unmarshal(result, &op.Result)
	return op, true, err
}

func (p *Postgres) BeginVarsUpdate(op nanoflare.VarsUpdateOperation, next nanoflare.Deployment, expected string) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Conditional activation and request reservation commit together.
	var workerID string
	err = tx.QueryRow(`SELECT id FROM workers WHERE id = $1 FOR UPDATE`, op.AppID).Scan(&workerID)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.ErrAppNotFound
	}
	if err != nil {
		return err
	}
	var count, percent int
	var deploymentID string
	err = tx.QueryRow(`SELECT count(*), COALESCE(max(id), ''), COALESCE(max(traffic_percent), 0) FROM deployments WHERE worker_id = $1 AND traffic_percent > 0`, op.AppID).Scan(&count, &deploymentID, &percent)
	if err != nil {
		return err
	}
	if count != 1 || deploymentID != expected || percent != 100 {
		return nanoflare.ErrDeploymentConflict
	}
	result, err := json.Marshal(op.Result)
	if err != nil {
		return err
	}
	inserted, err := tx.Exec(`INSERT INTO worker_vars_updates (worker_id, idempotency_key, fingerprint, status, result) VALUES ($1, $2, $3, 'pending', $4) ON CONFLICT DO NOTHING`, op.AppID, op.Key, op.Fingerprint, result)
	if err != nil {
		return err
	}
	n, err := inserted.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return nanoflare.ErrIdempotencyConflict
	}
	if err := activateDeploymentTx(tx, next); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Postgres) FinishVarsUpdate(appID, key, status string) error {
	result, err := p.db.Exec(`UPDATE worker_vars_updates SET status = $3 WHERE worker_id = $1 AND idempotency_key = $2`, appID, key, status)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return nanoflare.ErrAppNotFound
	}
	return err
}

func (p *Postgres) PendingVarsUpdates() ([]nanoflare.VarsUpdateOperation, error) {
	rows, err := p.db.Query(`SELECT worker_id,idempotency_key,fingerprint,status,result FROM worker_vars_updates WHERE status IN ('pending','rolling_back') ORDER BY worker_id,idempotency_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pending []nanoflare.VarsUpdateOperation
	for rows.Next() {
		var op nanoflare.VarsUpdateOperation
		var result []byte
		if err := rows.Scan(&op.AppID, &op.Key, &op.Fingerprint, &op.Status, &result); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(result, &op.Result); err != nil {
			return nil, err
		}
		pending = append(pending, op)
	}
	return pending, rows.Err()
}
