package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func (p *Postgres) UnreconciledObjectBuckets(ctx context.Context, bucketID, orgID string) ([]string, error) {
	query := `SELECT b.id FROM object_storage_buckets b
LEFT JOIN object_storage_bucket_metrics m ON m.bucket_id = b.id
WHERE NOT COALESCE(m.usage_reconciled, false)`
	var args []any
	if orgID != "" {
		query += ` AND b.org_id = $1`
		args = append(args, orgID)
	} else if bucketID != "" {
		query += ` AND b.id = $1`
		args = append(args, bucketID)
	}
	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var buckets []string
	for rows.Next() {
		var bucket string
		if err := rows.Scan(&bucket); err != nil {
			return nil, err
		}
		buckets = append(buckets, bucket)
	}
	return buckets, rows.Err()
}

func (p *Postgres) ObjectUsageRevision(ctx context.Context, bucketID string, advance bool) (int64, error) {
	var revision, increment int64
	if advance {
		increment = 1
	}
	err := p.db.QueryRowContext(ctx, `INSERT INTO object_storage_bucket_metrics (bucket_id, usage_revision)
VALUES ($1, $2) ON CONFLICT (bucket_id) DO UPDATE
SET usage_revision = object_storage_bucket_metrics.usage_revision + $2 RETURNING usage_revision`, bucketID, increment).Scan(&revision)
	return revision, err
}

func (p *Postgres) ReconcileObjectUsage(ctx context.Context, bucketID string, size, count int64) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO object_storage_bucket_metrics (bucket_id, size, object_count, usage_reconciled)
VALUES ($1, $2, $3, true)
ON CONFLICT (bucket_id) DO UPDATE SET size = EXCLUDED.size, object_count = EXCLUDED.object_count, usage_reconciled = true`, bucketID, size, count)
	return err
}

// Invalidate before storage I/O so a crash cannot leave an understated counter
// marked current. The caller holds the organization or bucket advisory lock.
func (p *Postgres) BeginObjectUsageChange(ctx context.Context, bucketID string) (bool, error) {
	var hadCurrentUsage bool
	err := p.db.QueryRowContext(ctx, `UPDATE object_storage_bucket_metrics SET usage_reconciled = false, usage_revision = usage_revision + 1
WHERE bucket_id = $1 AND usage_reconciled RETURNING true`, bucketID).Scan(&hadCurrentUsage)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return hadCurrentUsage, err
}

func (p *Postgres) FinishObjectUsageChange(ctx context.Context, bucketID string, bytes, count int64) error {
	_, err := p.db.ExecContext(ctx, `UPDATE object_storage_bucket_metrics
SET size = GREATEST(size + $2, 0), object_count = GREATEST(object_count + $3, 0), usage_reconciled = true WHERE bucket_id = $1`, bucketID, bytes, count)
	return err
}

// Commit the provider delta and retire its reservation in one transaction.
func (p *Postgres) FinishObjectMutation(ctx context.Context, bucketID, id string, bytes, count int64, transfer *nanoflare.ObjectTransfer) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM object_transfers WHERE id = $1`, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("object mutation reservation is missing")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE object_storage_bucket_metrics
SET size = GREATEST(size + $2, 0), object_count = GREATEST(object_count + $3, 0)
WHERE bucket_id = $1 AND usage_reconciled`, bucketID, bytes, count); err != nil {
		return err
	}
	if transfer != nil {
		data, err := json.Marshal(transfer)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE object_transfers SET transfer = $2 WHERE id = $1`, transfer.ID, string(data))
		if err != nil {
			return err
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			if err != nil {
				return err
			}
			return errors.New("object transfer session is missing")
		}
	}
	return tx.Commit()
}
