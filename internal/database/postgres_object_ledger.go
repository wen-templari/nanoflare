package database

import (
	"context"
	"database/sql"
	"errors"
)

func (p *Postgres) UnreconciledObjectBuckets(ctx context.Context, bucketID, orgID string) ([]string, error) {
	query := `SELECT b.id FROM object_storage_buckets b
LEFT JOIN object_storage_bucket_metrics m ON m.bucket_id = b.id
WHERE NOT COALESCE(m.usage_reconciled, false) AND `
	var selector string
	if orgID == "" {
		query += `b.id = $1`
		selector = bucketID
	} else {
		query += `b.org_id = $1`
		selector = orgID
	}
	rows, err := p.db.QueryContext(ctx, query, selector)
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
	err := p.db.QueryRowContext(ctx, `UPDATE object_storage_bucket_metrics SET usage_reconciled = false
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
