package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func (p *Postgres) migrateAssetUploads(ctx context.Context) error {
	_, err := p.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS asset_upload_sessions (
 id text PRIMARY KEY, org_id text NOT NULL, namespace_id text NOT NULL,
 revision bigint NOT NULL CHECK(revision>0), expires_at timestamptz NOT NULL, payload jsonb NOT NULL,
 FOREIGN KEY(namespace_id,org_id) REFERENCES dispatch_namespaces(id,org_id) ON DELETE CASCADE);
 CREATE INDEX IF NOT EXISTS asset_upload_org_expiry_idx ON asset_upload_sessions(org_id,expires_at);
 CREATE INDEX IF NOT EXISTS asset_upload_expiry_idx ON asset_upload_sessions(expires_at,id);`)
	return err
}
func (p *Postgres) GetAssetUploadSession(id string) (nanoflare.AssetUploadSession, error) {
	var session nanoflare.AssetUploadSession
	var data []byte
	err := p.db.QueryRow(`SELECT payload FROM asset_upload_sessions WHERE id=$1`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return session, nanoflare.ErrAssetUploadUnavailable
	}
	if err != nil {
		return session, err
	}
	err = json.Unmarshal(data, &session)
	return session, err
}
func (p *Postgres) SaveAssetUploadSession(session nanoflare.AssetUploadSession, expected int64) error {
	if expected < 0 || session.Revision != expected+1 {
		return nanoflare.ErrManagementConflict
	}
	result, err := p.db.Exec(`INSERT INTO asset_upload_sessions(id,org_id,namespace_id,revision,expires_at,payload)
 SELECT $1,$2,$3,$4,$5,$6 WHERE $7=0 OR EXISTS(SELECT 1 FROM asset_upload_sessions WHERE id=$1)
 ON CONFLICT(id) DO UPDATE SET revision=EXCLUDED.revision,expires_at=EXCLUDED.expires_at,payload=EXCLUDED.payload
 WHERE asset_upload_sessions.revision=$7 AND asset_upload_sessions.org_id=$2 AND asset_upload_sessions.namespace_id=$3`, session.ID, session.OrgID, session.NamespaceID, session.Revision, session.ExpiresAt, mustJSON(session), expected)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return nanoflare.ErrManagementConflict
	}
	return nil
}
func (p *Postgres) CountAssetUploadSessions(org string, now time.Time) (int, error) {
	var count int
	err := p.db.QueryRow(`SELECT count(*) FROM asset_upload_sessions WHERE org_id=$1 AND expires_at>$2`, org, now).Scan(&count)
	return count, err
}

func (p *Postgres) PruneAssetUploadSessions(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 256 {
		return 0, errors.New("asset cleanup limit must be 1-256")
	}
	result, err := p.db.ExecContext(ctx, `WITH expired AS (
 SELECT id FROM asset_upload_sessions WHERE expires_at<=$1 ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
 ) DELETE FROM asset_upload_sessions s USING expired e WHERE s.id=e.id`, now, limit)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	return int(count), err
}
