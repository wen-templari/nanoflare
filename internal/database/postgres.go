package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/clas/platform/internal/platform"
	_ "github.com/lib/pq"
)

type Postgres struct {
	db *sql.DB
}

func Open(ctx context.Context, databaseURL string) (*Postgres, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	store := &Postgres{db: db}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (p *Postgres) Close() error {
	return p.db.Close()
}

func (p *Postgres) migrate(ctx context.Context) error {
	_, err := p.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS apps (
	id text PRIMARY KEY,
	name text NOT NULL,
	hostname text NOT NULL UNIQUE,
	runtime_token text,
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS deployments (
	id text PRIMARY KEY,
	app_id text NOT NULL REFERENCES apps(id),
	files jsonb NOT NULL,
	entrypoint text NOT NULL,
	compatibility_date text NOT NULL,
	port integer NOT NULL,
	created_at timestamptz NOT NULL,
	active boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX IF NOT EXISTS deployments_active_app_idx
	ON deployments(app_id) WHERE active;
ALTER TABLE apps ADD COLUMN IF NOT EXISTS name text NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN IF NOT EXISTS runtime_token text;
UPDATE apps SET name = hostname WHERE name = '';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS files jsonb NOT NULL DEFAULT '[]';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS entrypoint text NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS format text NOT NULL DEFAULT '';
UPDATE deployments SET format = CASE
	WHEN jsonb_array_length(files) = 1 THEN 'service-worker'
	ELSE 'modules'
END WHERE format = '';
CREATE TABLE IF NOT EXISTS runtime_kv (
	app_id text NOT NULL REFERENCES apps(id),
	key text NOT NULL,
	value bytea NOT NULL,
	PRIMARY KEY (app_id, key)
);
DO $$
BEGIN
	IF EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema()
			AND table_name = 'runtime_kv'
			AND column_name = 'value'
			AND data_type = 'jsonb'
	) THEN
		ALTER TABLE runtime_kv
			ALTER COLUMN value TYPE bytea USING convert_to(value::text, 'UTF8');
	END IF;
END $$;`)
	if err != nil {
		return err
	}
	if err := p.migrateBundlePaths(ctx); err != nil {
		return err
	}
	if err := p.migrateRuntimeTokens(ctx); err != nil {
		return err
	}
	_, err = p.db.ExecContext(ctx, `
ALTER TABLE apps ALTER COLUMN runtime_token SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS apps_runtime_token_idx ON apps(runtime_token);
ALTER TABLE deployments DROP COLUMN IF EXISTS bundle_path;
ALTER TABLE deployments DROP COLUMN IF EXISTS capability_token;`)
	return err
}

func (p *Postgres) migrateRuntimeTokens(ctx context.Context) error {
	rows, err := p.db.QueryContext(ctx, `SELECT id FROM apps WHERE runtime_token IS NULL OR runtime_token = ''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		token, err := randomToken()
		if err != nil {
			return err
		}
		if _, err := p.db.ExecContext(ctx, `UPDATE apps SET runtime_token = $1 WHERE id = $2`, token, id); err != nil {
			return err
		}
	}
	return nil
}

func (p *Postgres) migrateBundlePaths(ctx context.Context) error {
	var exists bool
	err := p.db.QueryRowContext(ctx, `
SELECT EXISTS (
	SELECT 1 FROM information_schema.columns
	WHERE table_schema = current_schema() AND table_name = 'deployments' AND column_name = 'bundle_path'
)`).Scan(&exists)
	if err != nil || !exists {
		return err
	}
	rows, err := p.db.QueryContext(ctx, `SELECT id, bundle_path FROM deployments WHERE files = '[]'::jsonb`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, bundlePath string
		if err := rows.Scan(&id, &bundlePath); err != nil {
			return err
		}
		content, err := os.ReadFile(bundlePath)
		if err != nil {
			return fmt.Errorf("migrate deployment %s bundle %s: %w", id, bundlePath, err)
		}
		name := filepath.Base(bundlePath)
		files, err := json.Marshal([]platform.WorkerFile{{Name: name, Path: name, Size: int64(len(content)), Content: string(content)}})
		if err != nil {
			return err
		}
		if _, err := p.db.ExecContext(ctx, `UPDATE deployments SET files = $1, entrypoint = $2 WHERE id = $3`, files, name, id); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (p *Postgres) CreateApp(app platform.App) error {
	_, err := p.db.Exec(`INSERT INTO apps (id, name, hostname, runtime_token, created_at) VALUES ($1, $2, $3, $4, $5)`,
		app.ID, app.Name, app.Hostname, app.RuntimeToken, app.CreatedAt)
	if isUniqueViolation(err) {
		return platform.ErrAppExists
	}
	return err
}

func (p *Postgres) ListApps() ([]platform.App, error) {
	rows, err := p.db.Query(`SELECT id, name, hostname, runtime_token, created_at FROM apps ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var apps []platform.App
	for rows.Next() {
		var app platform.App
		if err := rows.Scan(&app.ID, &app.Name, &app.Hostname, &app.RuntimeToken, &app.CreatedAt); err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

func (p *Postgres) NextPort() (int, error) {
	var port int
	err := p.db.QueryRow(`SELECT COALESCE(MAX(port) + 1, 9001) FROM deployments`).Scan(&port)
	return port, err
}

func (p *Postgres) Activate(deployment platform.Deployment) error {
	files, err := json.Marshal(deployment.Files)
	if err != nil {
		return err
	}
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE deployments SET active = false WHERE app_id = $1 AND active`, deployment.AppID)
	if err != nil {
		return err
	}
	_ = result
	_, err = tx.Exec(`
INSERT INTO deployments
	(id, app_id, files, entrypoint, format, compatibility_date, port, created_at, active)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, true)`,
		deployment.ID, deployment.AppID, files, deployment.Entrypoint, deployment.Format,
		deployment.CompatibilityDate, deployment.Port, deployment.CreatedAt)
	if isForeignKeyViolation(err) {
		return platform.ErrAppNotFound
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Postgres) SetActive(appID, deploymentID string) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE deployments SET active = false WHERE app_id = $1 AND active`, appID); err != nil {
		return err
	}
	if deploymentID == "" {
		return tx.Commit()
	}
	result, err := tx.Exec(`UPDATE deployments SET active = true WHERE app_id = $1 AND id = $2`, appID, deploymentID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return errors.New("deployment not found")
	}
	return tx.Commit()
}

func (p *Postgres) ActiveDeployments() ([]platform.ActiveDeployment, error) {
	rows, err := p.db.Query(`
SELECT a.id, a.name, a.hostname, a.runtime_token, a.created_at,
	d.id, d.app_id, d.files, d.entrypoint, d.format, d.compatibility_date, d.port, d.created_at
FROM deployments d
JOIN apps a ON a.id = d.app_id
WHERE d.active
ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var active []platform.ActiveDeployment
	for rows.Next() {
		var item platform.ActiveDeployment
		var files []byte
		err := rows.Scan(
			&item.App.ID, &item.App.Name, &item.App.Hostname, &item.App.RuntimeToken, &item.App.CreatedAt,
			&item.Deployment.ID, &item.Deployment.AppID, &files, &item.Deployment.Entrypoint,
			&item.Deployment.Format, &item.Deployment.CompatibilityDate, &item.Deployment.Port,
			&item.Deployment.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(files, &item.Deployment.Files); err != nil {
			return nil, err
		}
		active = append(active, item)
	}
	return active, rows.Err()
}

func (p *Postgres) ListDeployments() ([]platform.DeploymentRecord, error) {
	rows, err := p.db.Query(`
	SELECT a.id, a.name, a.hostname, a.runtime_token, a.created_at,
		d.id, d.app_id, d.files, d.entrypoint, d.format, d.compatibility_date, d.port, d.created_at, d.active
	FROM deployments d
	JOIN apps a ON a.id = d.app_id
	ORDER BY d.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []platform.DeploymentRecord
	for rows.Next() {
		var item platform.DeploymentRecord
		var files []byte
		err := rows.Scan(
			&item.App.ID, &item.App.Name, &item.App.Hostname, &item.App.RuntimeToken, &item.App.CreatedAt,
			&item.Deployment.ID, &item.Deployment.AppID, &files, &item.Deployment.Entrypoint,
			&item.Deployment.Format, &item.Deployment.CompatibilityDate, &item.Deployment.Port,
			&item.Deployment.CreatedAt, &item.Active,
		)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(files, &item.Deployment.Files); err != nil {
			return nil, err
		}
		records = append(records, item)
	}
	return records, rows.Err()
}

func (p *Postgres) AppIDForCapability(capability string) (string, error) {
	var appID string
	err := p.db.QueryRow(`SELECT id FROM apps WHERE runtime_token = $1`, capability).Scan(&appID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", platform.ErrInvalidCapability
	}
	return appID, err
}

func (p *Postgres) KVGet(capability, key string) ([]byte, bool, error) {
	appID, err := p.AppIDForCapability(capability)
	if err != nil {
		return nil, false, err
	}
	var value []byte
	err = p.db.QueryRow(`SELECT value FROM runtime_kv WHERE app_id = $1 AND key = $2`, appID, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return value, err == nil, err
}

func (p *Postgres) KVPut(capability, key string, value []byte) error {
	appID, err := p.AppIDForCapability(capability)
	if err != nil {
		return err
	}
	_, err = p.db.Exec(`
INSERT INTO runtime_kv (app_id, key, value) VALUES ($1, $2, $3)
ON CONFLICT (app_id, key) DO UPDATE SET value = EXCLUDED.value`, appID, key, value)
	return err
}

func randomToken() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func (p *Postgres) KVDelete(capability, key string) error {
	appID, err := p.AppIDForCapability(capability)
	if err != nil {
		return err
	}
	_, err = p.db.Exec(`DELETE FROM runtime_kv WHERE app_id = $1 AND key = $2`, appID, key)
	return err
}

func isUniqueViolation(err error) bool {
	return err != nil && sqlState(err) == "23505"
}

func isForeignKeyViolation(err error) bool {
	return err != nil && sqlState(err) == "23503"
}

func sqlState(err error) string {
	type sqlStater interface {
		SQLState() string
	}
	var target sqlStater
	if errors.As(err, &target) {
		return target.SQLState()
	}
	return fmt.Sprint(err)
}
