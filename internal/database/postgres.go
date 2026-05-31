package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

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
	hostname text NOT NULL UNIQUE,
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS deployments (
	id text PRIMARY KEY,
	app_id text NOT NULL REFERENCES apps(id),
	bundle_path text NOT NULL,
	compatibility_date text NOT NULL,
	port integer NOT NULL,
	capability_token text NOT NULL UNIQUE,
	created_at timestamptz NOT NULL,
	active boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX IF NOT EXISTS deployments_active_app_idx
	ON deployments(app_id) WHERE active;
CREATE TABLE IF NOT EXISTS runtime_kv (
	app_id text NOT NULL REFERENCES apps(id),
	key text NOT NULL,
	value jsonb NOT NULL,
	PRIMARY KEY (app_id, key)
);`)
	return err
}

func (p *Postgres) CreateApp(app platform.App) error {
	_, err := p.db.Exec(`INSERT INTO apps (id, hostname, created_at) VALUES ($1, $2, $3)`,
		app.ID, app.Hostname, app.CreatedAt)
	if isUniqueViolation(err) {
		return platform.ErrAppExists
	}
	return err
}

func (p *Postgres) ListApps() ([]platform.App, error) {
	rows, err := p.db.Query(`SELECT id, hostname, created_at FROM apps ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var apps []platform.App
	for rows.Next() {
		var app platform.App
		if err := rows.Scan(&app.ID, &app.Hostname, &app.CreatedAt); err != nil {
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
	(id, app_id, bundle_path, compatibility_date, port, capability_token, created_at, active)
VALUES ($1, $2, $3, $4, $5, $6, $7, true)`,
		deployment.ID, deployment.AppID, deployment.BundlePath, deployment.CompatibilityDate,
		deployment.Port, deployment.CapabilityToken, deployment.CreatedAt)
	if isForeignKeyViolation(err) {
		return platform.ErrAppNotFound
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Postgres) ActiveDeployments() ([]platform.ActiveDeployment, error) {
	rows, err := p.db.Query(`
SELECT a.id, a.hostname, a.created_at,
	d.id, d.app_id, d.bundle_path, d.compatibility_date, d.port, d.capability_token, d.created_at
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
		err := rows.Scan(
			&item.App.ID, &item.App.Hostname, &item.App.CreatedAt,
			&item.Deployment.ID, &item.Deployment.AppID, &item.Deployment.BundlePath,
			&item.Deployment.CompatibilityDate, &item.Deployment.Port,
			&item.Deployment.CapabilityToken, &item.Deployment.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		active = append(active, item)
	}
	return active, rows.Err()
}

func (p *Postgres) AppIDForCapability(capability string) (string, error) {
	var appID string
	err := p.db.QueryRow(`SELECT app_id FROM deployments WHERE capability_token = $1 AND active`, capability).Scan(&appID)
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
	if !json.Valid(value) {
		return errors.New("value must be valid JSON")
	}
	_, err = p.db.Exec(`
INSERT INTO runtime_kv (app_id, key, value) VALUES ($1, $2, $3)
ON CONFLICT (app_id, key) DO UPDATE SET value = EXCLUDED.value`, appID, key, value)
	return err
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
