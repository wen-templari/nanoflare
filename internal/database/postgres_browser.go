package database

import (
	"context"
	"database/sql"
	"errors"
	"github.com/clas/nanoflare/internal/browserstore"
	"time"
)

// UpdateBrowserRecord holds a transaction-scoped advisory lock, including for
// absent records. All instances must use this method when updating browser state.
func (p *Postgres) UpdateBrowserRecord(ctx context.Context, key string, fn func([]byte) ([]byte, time.Time, error)) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 87))`, key); err != nil {
		return err
	}
	var data []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM oidc_browser_records WHERE id=$1 AND expires_at > now()`, key).Scan(&data)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	data, expires, err := fn(data)
	if errors.Is(err, browserstore.ErrUnchanged) {
		return nil
	}
	if err != nil {
		return err
	}
	if data == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM oidc_browser_records WHERE id=$1`, key)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO oidc_browser_records(id,payload,expires_at) VALUES($1,$2,$3) ON CONFLICT(id) DO UPDATE SET payload=EXCLUDED.payload,expires_at=EXCLUDED.expires_at`, key, data, expires)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (p *Postgres) CleanupBrowserRecords(ctx context.Context) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM oidc_browser_records WHERE expires_at <= now()`)
	return err
}

func (p *Postgres) Persistent() bool { return true }
func (p *Postgres) ReadBrowserRecord(ctx context.Context, key string) ([]byte, error) {
	var data []byte
	err := p.db.QueryRowContext(ctx, `SELECT payload FROM oidc_browser_records WHERE id=$1 AND expires_at > now()`, key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return data, err
}
