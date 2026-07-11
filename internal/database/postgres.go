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

	"github.com/clas/nanoflare/internal/nanoflare"
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
	org_id text NOT NULL DEFAULT '',
	name text NOT NULL,
	hostname text NOT NULL UNIQUE,
	auth jsonb NOT NULL DEFAULT '{}'::jsonb,
	runtime_token text,
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS users (
	id text PRIMARY KEY,
	email text NOT NULL UNIQUE,
	password_hash bytea NOT NULL,
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS organizations (
	id text PRIMARY KEY,
	name text NOT NULL,
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS user_organizations (
	user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	created_at timestamptz NOT NULL DEFAULT NOW(),
	PRIMARY KEY (user_id, organization_id)
);
CREATE TABLE IF NOT EXISTS deployments (
	id text PRIMARY KEY,
	app_id text NOT NULL REFERENCES apps(id),
	files jsonb NOT NULL,
	assets jsonb NOT NULL DEFAULT '[]',
	entrypoint text NOT NULL,
	format text NOT NULL DEFAULT '',
	compatibility_date text NOT NULL,
	vars jsonb NOT NULL DEFAULT '{}'::jsonb,
	kv_namespaces jsonb NOT NULL DEFAULT '[]'::jsonb,
	object_storage_bucket jsonb NOT NULL DEFAULT '[]'::jsonb,
	asset_config jsonb NOT NULL DEFAULT '{}'::jsonb,
	bundle_size bigint NOT NULL DEFAULT 0,
	object_key text NOT NULL DEFAULT '',
	port integer NOT NULL,
	created_at timestamptz NOT NULL,
	active boolean NOT NULL DEFAULT false
);
CREATE TABLE IF NOT EXISTS kv_namespaces (
	id text PRIMARY KEY,
	org_id text NOT NULL DEFAULT '',
	name text NOT NULL UNIQUE,
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS object_storage_buckets (
	id text PRIMARY KEY,
	org_id text NOT NULL DEFAULT '',
	name text NOT NULL UNIQUE,
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS app_secrets (
	app_id text NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
	name text NOT NULL,
	nonce bytea NOT NULL,
	ciphertext bytea NOT NULL,
	created_at timestamptz NOT NULL,
	updated_at timestamptz NOT NULL,
	PRIMARY KEY (app_id, name)
);
CREATE UNIQUE INDEX IF NOT EXISTS deployments_active_app_idx
	ON deployments(app_id) WHERE active;
ALTER TABLE apps ADD COLUMN IF NOT EXISTS name text NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN IF NOT EXISTS org_id text NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN IF NOT EXISTS auth jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE apps ADD COLUMN IF NOT EXISTS runtime_token text;
UPDATE apps SET name = hostname WHERE name = '';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS files jsonb NOT NULL DEFAULT '[]';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS assets jsonb NOT NULL DEFAULT '[]';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS entrypoint text NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS format text NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS vars jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS kv_namespaces jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS object_storage_bucket jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS asset_config jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS bundle_size bigint NOT NULL DEFAULT 0;
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS object_key text NOT NULL DEFAULT '';
ALTER TABLE kv_namespaces ADD COLUMN IF NOT EXISTS org_id text NOT NULL DEFAULT '';
ALTER TABLE object_storage_buckets ADD COLUMN IF NOT EXISTS org_id text NOT NULL DEFAULT '';
UPDATE deployments SET format = CASE
	WHEN jsonb_array_length(files) = 1 THEN 'service-worker'
	ELSE 'modules'
END WHERE format = '';
UPDATE deployments SET bundle_size = COALESCE((
	SELECT SUM(COALESCE((entry->>'size')::bigint, 0))
	FROM jsonb_array_elements(files) AS entry
), 0) WHERE bundle_size = 0;
CREATE TABLE IF NOT EXISTS runtime_kv (
	kv_namespace_id text NOT NULL REFERENCES kv_namespaces(id),
	key text NOT NULL,
	value bytea NOT NULL,
	PRIMARY KEY (kv_namespace_id, key)
);
CREATE TABLE IF NOT EXISTS kv_namespace_metrics (
	kv_namespace_id text PRIMARY KEY REFERENCES kv_namespaces(id) ON DELETE CASCADE,
	reads bigint NOT NULL DEFAULT 0,
	writes bigint NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS object_storage_bucket_metrics (
	bucket_id text PRIMARY KEY REFERENCES object_storage_buckets(id) ON DELETE CASCADE,
	reads bigint NOT NULL DEFAULT 0,
	writes bigint NOT NULL DEFAULT 0,
	size bigint NOT NULL DEFAULT 0
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
	if err := p.migrateKVNamespaces(ctx); err != nil {
		return err
	}
	_, err = p.db.ExecContext(ctx, `
ALTER TABLE kv_namespaces DROP CONSTRAINT IF EXISTS kv_namespaces_name_key;
ALTER TABLE object_storage_buckets DROP CONSTRAINT IF EXISTS object_storage_buckets_name_key;
CREATE UNIQUE INDEX IF NOT EXISTS kv_namespaces_org_name_idx ON kv_namespaces(org_id, name);
CREATE UNIQUE INDEX IF NOT EXISTS object_storage_buckets_org_name_idx ON object_storage_buckets(org_id, name);
ALTER TABLE apps ALTER COLUMN runtime_token SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS apps_runtime_token_idx ON apps(runtime_token);
ALTER TABLE deployments DROP COLUMN IF EXISTS bundle_path;
ALTER TABLE deployments DROP COLUMN IF EXISTS capability_token;`)
	return err
}

func (p *Postgres) migrateKVNamespaces(ctx context.Context) error {
	hasAppID, err := p.columnExists(ctx, "runtime_kv", "app_id")
	if err != nil {
		return err
	}
	hasNamespaceID, err := p.columnExists(ctx, "runtime_kv", "kv_namespace_id")
	if err != nil {
		return err
	}
	if !hasAppID {
		return nil
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if hasAppID && !hasNamespaceID {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE runtime_kv ADD COLUMN kv_namespace_id text`); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM apps ORDER BY id`)
	if err != nil {
		return err
	}
	var appIDs []string
	for rows.Next() {
		var appID string
		if err := rows.Scan(&appID); err != nil {
			rows.Close()
			return err
		}
		appIDs = append(appIDs, appID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, appID := range appIDs {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO kv_namespaces (id, name, created_at)
VALUES ($1, $2, NOW())
ON CONFLICT (id) DO NOTHING`,
			legacyKVNamespaceID(appID), legacyKVNamespaceName(appID)); err != nil {
			return err
		}
		if hasAppID {
			if _, err := tx.ExecContext(ctx, `
UPDATE runtime_kv
SET kv_namespace_id = $1
WHERE app_id = $2 AND (kv_namespace_id IS NULL OR kv_namespace_id = '')`,
				legacyKVNamespaceID(appID), appID); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM runtime_kv WHERE kv_namespace_id IS NULL OR kv_namespace_id = ''`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE runtime_kv DROP CONSTRAINT IF EXISTS runtime_kv_pkey`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
DO $$
BEGIN
	IF NOT EXISTS (
		SELECT 1 FROM pg_constraint
		WHERE conname = 'runtime_kv_namespace_fk'
	) THEN
		ALTER TABLE runtime_kv
			ADD CONSTRAINT runtime_kv_namespace_fk
			FOREIGN KEY (kv_namespace_id) REFERENCES kv_namespaces(id);
	END IF;
END $$;`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE runtime_kv ALTER COLUMN kv_namespace_id SET NOT NULL`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE runtime_kv ADD PRIMARY KEY (kv_namespace_id, key)`); err != nil {
		return err
	}
	if hasAppID {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE runtime_kv DROP COLUMN app_id`); err != nil {
			return err
		}
	}
	return tx.Commit()
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
		files, err := json.Marshal([]nanoflare.WorkerFile{{Name: name, Path: name, Size: int64(len(content)), Content: string(content)}})
		if err != nil {
			return err
		}
		if _, err := p.db.ExecContext(ctx, `UPDATE deployments SET files = $1, entrypoint = $2 WHERE id = $3`, files, name, id); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (p *Postgres) CreateUser(user nanoflare.User) error {
	_, err := p.db.Exec(`INSERT INTO users (id, email, password_hash, created_at) VALUES ($1, $2, $3, $4)`,
		user.ID, user.Email, user.PasswordHash, user.CreatedAt)
	if isUniqueViolation(err) {
		return nanoflare.ErrUserExists
	}
	return err
}

func (p *Postgres) UserByEmail(email string) (nanoflare.User, error) {
	var user nanoflare.User
	err := p.db.QueryRow(`SELECT id, email, password_hash, created_at FROM users WHERE email = $1`, email).
		Scan(&user.ID, &user.Email, &user.PasswordHash, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.User{}, nanoflare.ErrUserNotFound
	}
	return user, err
}

func (p *Postgres) UserCount() (int, error) {
	var count int
	err := p.db.QueryRow(`SELECT count(*) FROM users`).Scan(&count)
	return count, err
}

func (p *Postgres) CreateOrganization(org nanoflare.Organization) error {
	_, err := p.db.Exec(`INSERT INTO organizations (id, name, created_at) VALUES ($1, $2, $3)`,
		org.ID, org.Name, org.CreatedAt)
	if isUniqueViolation(err) {
		return nanoflare.ErrOrganizationExists
	}
	return err
}

func (p *Postgres) GetOrganization(orgID string) (nanoflare.Organization, error) {
	var org nanoflare.Organization
	err := p.db.QueryRow(`SELECT id, name, created_at FROM organizations WHERE id = $1`, orgID).Scan(&org.ID, &org.Name, &org.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.Organization{}, nanoflare.ErrOrganizationNotFound
	}
	return org, err
}

func (p *Postgres) AddUserToOrganization(userID, orgID string) error {
	_, err := p.db.Exec(`
INSERT INTO user_organizations (user_id, organization_id)
VALUES ($1, $2)
ON CONFLICT (user_id, organization_id) DO NOTHING`, userID, orgID)
	if isForeignKeyViolation(err) {
		return nanoflare.ErrMembershipNotFound
	}
	return err
}

func (p *Postgres) ListOrganizationsForUser(userID string) ([]nanoflare.Organization, error) {
	rows, err := p.db.Query(`
SELECT o.id, o.name, o.created_at
FROM organizations o
JOIN user_organizations uo ON uo.organization_id = o.id
WHERE uo.user_id = $1
ORDER BY o.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orgs := []nanoflare.Organization{}
	for rows.Next() {
		var org nanoflare.Organization
		if err := rows.Scan(&org.ID, &org.Name, &org.CreatedAt); err != nil {
			return nil, err
		}
		orgs = append(orgs, org)
	}
	return orgs, rows.Err()
}

func (p *Postgres) UserBelongsToOrganization(userID, orgID string) (bool, error) {
	var exists bool
	err := p.db.QueryRow(`
SELECT EXISTS (
	SELECT 1 FROM user_organizations
	WHERE user_id = $1 AND organization_id = $2
)`, userID, orgID).Scan(&exists)
	return exists, err
}

func (p *Postgres) CreateApp(app nanoflare.App) error {
	_, err := p.db.Exec(`INSERT INTO apps (id, org_id, name, hostname, auth, runtime_token, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		app.ID, app.OrgID, app.Name, app.Hostname, mustJSON(app.Auth), app.RuntimeToken, app.CreatedAt)
	if isUniqueViolation(err) {
		return nanoflare.ErrAppExists
	}
	return err
}

func (p *Postgres) CreateKVNamespace(namespace nanoflare.KVNamespace) error {
	_, err := p.db.Exec(`INSERT INTO kv_namespaces (id, org_id, name, created_at) VALUES ($1, $2, $3, $4)`,
		namespace.ID, namespace.OrgID, namespace.Name, namespace.CreatedAt)
	if isUniqueViolation(err) {
		return nanoflare.ErrKVNamespaceExists
	}
	return err
}

func (p *Postgres) ListKVNamespaces() ([]nanoflare.KVNamespace, error) {
	return p.ListKVNamespacesByOrg("")
}

func (p *Postgres) ListKVNamespacesByOrg(orgID string) ([]nanoflare.KVNamespace, error) {
	query := `SELECT id, org_id, name, created_at FROM kv_namespaces`
	var rows *sql.Rows
	var err error
	if orgID == "" {
		rows, err = p.db.Query(query + ` ORDER BY name`)
	} else {
		rows, err = p.db.Query(query+` WHERE org_id = $1 ORDER BY name`, orgID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	namespaces := make([]nanoflare.KVNamespace, 0)
	for rows.Next() {
		var namespace nanoflare.KVNamespace
		if err := rows.Scan(&namespace.ID, &namespace.OrgID, &namespace.Name, &namespace.CreatedAt); err != nil {
			return nil, err
		}
		namespaces = append(namespaces, namespace)
	}
	return namespaces, rows.Err()
}

func (p *Postgres) GetKVNamespace(namespaceID string) (nanoflare.KVNamespace, error) {
	var namespace nanoflare.KVNamespace
	err := p.db.QueryRow(`SELECT id, org_id, name, created_at FROM kv_namespaces WHERE id = $1`, namespaceID).
		Scan(&namespace.ID, &namespace.OrgID, &namespace.Name, &namespace.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.KVNamespace{}, nanoflare.ErrKVNamespaceNotFound
	}
	return namespace, err
}

func (p *Postgres) UpdateKVNamespace(namespace nanoflare.KVNamespace) error {
	result, err := p.db.Exec(`UPDATE kv_namespaces SET name = $2 WHERE id = $1`, namespace.ID, namespace.Name)
	if isUniqueViolation(err) {
		return nanoflare.ErrKVNamespaceExists
	}
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return nanoflare.ErrKVNamespaceNotFound
	}
	return nil
}

func (p *Postgres) CreateObjectStorageBucket(bucket nanoflare.ObjectStorageBucket) error {
	_, err := p.db.Exec(`INSERT INTO object_storage_buckets (id, org_id, name, created_at) VALUES ($1, $2, $3, $4)`,
		bucket.ID, bucket.OrgID, bucket.Name, bucket.CreatedAt)
	if isUniqueViolation(err) {
		return nanoflare.ErrObjectStorageBucketExists
	}
	return err
}

func (p *Postgres) ListObjectStorageBuckets() ([]nanoflare.ObjectStorageBucket, error) {
	return p.ListObjectStorageBucketsByOrg("")
}

func (p *Postgres) ListObjectStorageBucketsByOrg(orgID string) ([]nanoflare.ObjectStorageBucket, error) {
	query := `SELECT id, org_id, name, created_at FROM object_storage_buckets`
	var rows *sql.Rows
	var err error
	if orgID == "" {
		rows, err = p.db.Query(query + ` ORDER BY name`)
	} else {
		rows, err = p.db.Query(query+` WHERE org_id = $1 ORDER BY name`, orgID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	buckets := make([]nanoflare.ObjectStorageBucket, 0)
	for rows.Next() {
		var bucket nanoflare.ObjectStorageBucket
		if err := rows.Scan(&bucket.ID, &bucket.OrgID, &bucket.Name, &bucket.CreatedAt); err != nil {
			return nil, err
		}
		buckets = append(buckets, bucket)
	}
	return buckets, rows.Err()
}

func (p *Postgres) GetObjectStorageBucket(bucketID string) (nanoflare.ObjectStorageBucket, error) {
	var bucket nanoflare.ObjectStorageBucket
	err := p.db.QueryRow(`SELECT id, org_id, name, created_at FROM object_storage_buckets WHERE id = $1`, bucketID).
		Scan(&bucket.ID, &bucket.OrgID, &bucket.Name, &bucket.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.ObjectStorageBucket{}, nanoflare.ErrObjectStorageBucketNotFound
	}
	return bucket, err
}

func (p *Postgres) UpdateObjectStorageBucket(bucket nanoflare.ObjectStorageBucket) error {
	result, err := p.db.Exec(`UPDATE object_storage_buckets SET name = $2 WHERE id = $1`, bucket.ID, bucket.Name)
	if isUniqueViolation(err) {
		return nanoflare.ErrObjectStorageBucketExists
	}
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return nanoflare.ErrObjectStorageBucketNotFound
	}
	return nil
}

func (p *Postgres) ListApps() ([]nanoflare.App, error) {
	return p.ListAppsByOrg("")
}

func (p *Postgres) ListAppsByOrg(orgID string) ([]nanoflare.App, error) {
	query := `SELECT id, org_id, name, hostname, auth, runtime_token, created_at FROM apps`
	var rows *sql.Rows
	var err error
	if orgID == "" {
		rows, err = p.db.Query(query + ` ORDER BY id`)
	} else {
		rows, err = p.db.Query(query+` WHERE org_id = $1 ORDER BY id`, orgID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	apps := make([]nanoflare.App, 0)
	for rows.Next() {
		var app nanoflare.App
		var auth []byte
		if err := rows.Scan(&app.ID, &app.OrgID, &app.Name, &app.Hostname, &auth, &app.RuntimeToken, &app.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(auth, &app.Auth); err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

func (p *Postgres) getApp(appID string) (nanoflare.App, error) {
	var app nanoflare.App
	var auth []byte
	err := p.db.QueryRow(`SELECT id, org_id, name, hostname, auth, runtime_token, created_at FROM apps WHERE id = $1`, appID).
		Scan(&app.ID, &app.OrgID, &app.Name, &app.Hostname, &auth, &app.RuntimeToken, &app.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.App{}, nanoflare.ErrAppNotFound
	}
	if err != nil {
		return nanoflare.App{}, err
	}
	if err := json.Unmarshal(auth, &app.Auth); err != nil {
		return nanoflare.App{}, err
	}
	return app, nil
}

func (p *Postgres) UpdateApp(app nanoflare.App) error {
	result, err := p.db.Exec(`UPDATE apps SET name = $2, hostname = $3, auth = $4 WHERE id = $1`,
		app.ID, app.Name, app.Hostname, mustJSON(app.Auth))
	if isUniqueViolation(err) {
		return nanoflare.ErrAppExists
	}
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return nanoflare.ErrAppNotFound
	}
	return nil
}

func (p *Postgres) DeleteApp(appID string) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM deployments WHERE app_id = $1`, appID); err != nil {
		return err
	}
	result, err := tx.Exec(`DELETE FROM apps WHERE id = $1`, appID)
	if err != nil {
		return err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if deleted == 0 {
		return nanoflare.ErrAppNotFound
	}
	return tx.Commit()
}

func (p *Postgres) ListSecrets(appID string) ([]nanoflare.SecretRecord, error) {
	if _, err := p.getApp(appID); err != nil {
		return nil, err
	}
	rows, err := p.db.Query(`SELECT name, nonce, ciphertext, created_at, updated_at FROM app_secrets WHERE app_id = $1 ORDER BY name`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []nanoflare.SecretRecord{}
	for rows.Next() {
		var record nanoflare.SecretRecord
		if err := rows.Scan(&record.Name, &record.Nonce, &record.Ciphertext, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (p *Postgres) PutSecret(appID string, secret nanoflare.SecretRecord) error {
	_, err := p.db.Exec(`
INSERT INTO app_secrets (app_id, name, nonce, ciphertext, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (app_id, name)
DO UPDATE SET nonce = EXCLUDED.nonce, ciphertext = EXCLUDED.ciphertext, updated_at = EXCLUDED.updated_at`,
		appID, secret.Name, secret.Nonce, secret.Ciphertext, secret.CreatedAt, secret.UpdatedAt)
	if isForeignKeyViolation(err) {
		return nanoflare.ErrAppNotFound
	}
	return err
}

func (p *Postgres) DeleteSecret(appID, name string) error {
	result, err := p.db.Exec(`DELETE FROM app_secrets WHERE app_id = $1 AND name = $2`, appID, name)
	if err != nil {
		return err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if deleted == 0 {
		if _, err := p.getApp(appID); err != nil {
			return err
		}
		return nanoflare.ErrSecretNotFound
	}
	return nil
}

func (p *Postgres) DeleteKVNamespace(namespaceID string) error {
	var inUse bool
	err := p.db.QueryRow(`
SELECT EXISTS (
	SELECT 1
	FROM deployments d, jsonb_array_elements(d.kv_namespaces) AS binding
	WHERE binding->>'id' = $1
)`, namespaceID).Scan(&inUse)
	if err != nil {
		return err
	}
	if inUse {
		return nanoflare.ErrKVNamespaceInUse
	}
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM runtime_kv WHERE kv_namespace_id = $1`, namespaceID); err != nil {
		return err
	}
	result, err := tx.Exec(`DELETE FROM kv_namespaces WHERE id = $1`, namespaceID)
	if err != nil {
		return err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if deleted == 0 {
		return nanoflare.ErrKVNamespaceNotFound
	}
	return tx.Commit()
}

func (p *Postgres) DeleteObjectStorageBucket(bucketID string) error {
	var inUse bool
	err := p.db.QueryRow(`
SELECT EXISTS (
	SELECT 1
	FROM deployments d, jsonb_array_elements(d.object_storage_bucket) AS binding
	WHERE binding->>'bucket_id' = $1
)`, bucketID).Scan(&inUse)
	if err != nil {
		return err
	}
	if inUse {
		return nanoflare.ErrObjectStorageBucketInUse
	}
	result, err := p.db.Exec(`DELETE FROM object_storage_buckets WHERE id = $1`, bucketID)
	if err != nil {
		return err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if deleted == 0 {
		return nanoflare.ErrObjectStorageBucketNotFound
	}
	return nil
}

func (p *Postgres) NextPort() (int, error) {
	var port int
	err := p.db.QueryRow(`SELECT COALESCE(MAX(port) + 1, 9001) FROM deployments`).Scan(&port)
	return port, err
}

func (p *Postgres) Activate(deployment nanoflare.Deployment) error {
	files := []byte(`[]`)
	assets, err := json.Marshal(deployment.Assets)
	if err != nil {
		return err
	}
	if deployment.ObjectKey == "" {
		files, err = json.Marshal(deployment.Files)
		if err != nil {
			return err
		}
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
	(id, app_id, files, assets, entrypoint, format, compatibility_date, vars, kv_namespaces, object_storage_bucket, asset_config, bundle_size, object_key, port, created_at, active)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, true)`,
		deployment.ID, deployment.AppID, files, assets, deployment.Entrypoint, deployment.Format,
		deployment.CompatibilityDate, mustJSON(deployment.Vars), mustJSON(deployment.KVNamespaces), mustJSON(deployment.ObjectStorageBuckets), mustJSON(deployment.AssetConfig), deployment.BundleSize, deployment.ObjectKey, deployment.Port, deployment.CreatedAt)
	if isForeignKeyViolation(err) {
		return nanoflare.ErrAppNotFound
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

func (p *Postgres) DeleteDeployment(id string) error {
	_, err := p.db.Exec(`DELETE FROM deployments WHERE id = $1`, id)
	return err
}

func (p *Postgres) ActiveDeployments() ([]nanoflare.ActiveDeployment, error) {
	rows, err := p.db.Query(`
SELECT a.id, a.org_id, a.name, a.hostname, a.auth, a.runtime_token, a.created_at,
	d.id, d.app_id, d.files, d.assets, d.entrypoint, d.format, d.compatibility_date, d.vars, d.kv_namespaces, d.object_storage_bucket, d.asset_config, d.bundle_size, d.object_key, d.port, d.created_at
FROM deployments d
JOIN apps a ON a.id = d.app_id
WHERE d.active
ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var active []nanoflare.ActiveDeployment
	for rows.Next() {
		var item nanoflare.ActiveDeployment
		var files, assets, vars, kvNamespaces, objectStorageBuckets, assetConfig, auth []byte
		err := rows.Scan(
			&item.App.ID, &item.App.OrgID, &item.App.Name, &item.App.Hostname, &auth, &item.App.RuntimeToken, &item.App.CreatedAt,
			&item.Deployment.ID, &item.Deployment.AppID, &files, &assets, &item.Deployment.Entrypoint,
			&item.Deployment.Format, &item.Deployment.CompatibilityDate, &vars, &kvNamespaces, &objectStorageBuckets, &assetConfig, &item.Deployment.BundleSize,
			&item.Deployment.ObjectKey, &item.Deployment.Port,
			&item.Deployment.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(files, &item.Deployment.Files); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(auth, &item.App.Auth); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(assets, &item.Deployment.Assets); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(vars, &item.Deployment.Vars); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(kvNamespaces, &item.Deployment.KVNamespaces); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(objectStorageBuckets, &item.Deployment.ObjectStorageBuckets); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(assetConfig, &item.Deployment.AssetConfig); err != nil {
			return nil, err
		}
		active = append(active, item)
	}
	return active, rows.Err()
}

func (p *Postgres) ListDeployments() ([]nanoflare.DeploymentRecord, error) {
	rows, err := p.db.Query(`
	SELECT a.id, a.org_id, a.name, a.hostname, a.auth, a.runtime_token, a.created_at,
		d.id, d.app_id, d.assets, d.entrypoint, d.format, d.compatibility_date, d.vars, d.kv_namespaces, d.object_storage_bucket, d.asset_config, d.bundle_size, d.object_key, d.port, d.created_at, d.active
	FROM deployments d
	JOIN apps a ON a.id = d.app_id
	ORDER BY d.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []nanoflare.DeploymentRecord
	for rows.Next() {
		var item nanoflare.DeploymentRecord
		var assets, vars, kvNamespaces, objectStorageBuckets, assetConfig, auth []byte
		err := rows.Scan(
			&item.App.ID, &item.App.OrgID, &item.App.Name, &item.App.Hostname, &auth, &item.App.RuntimeToken, &item.App.CreatedAt,
			&item.Deployment.ID, &item.Deployment.AppID, &assets, &item.Deployment.Entrypoint,
			&item.Deployment.Format, &item.Deployment.CompatibilityDate, &vars, &kvNamespaces, &objectStorageBuckets, &assetConfig, &item.Deployment.BundleSize,
			&item.Deployment.ObjectKey, &item.Deployment.Port,
			&item.Deployment.CreatedAt, &item.Active,
		)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(assets, &item.Deployment.Assets); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(vars, &item.Deployment.Vars); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(auth, &item.App.Auth); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(kvNamespaces, &item.Deployment.KVNamespaces); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(objectStorageBuckets, &item.Deployment.ObjectStorageBuckets); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(assetConfig, &item.Deployment.AssetConfig); err != nil {
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
		return "", nanoflare.ErrInvalidCapability
	}
	return appID, err
}

func (p *Postgres) KVGet(capability, namespaceID, key string) ([]byte, bool, error) {
	if _, err := p.AppIDForCapability(capability); err != nil {
		return nil, false, err
	}
	if _, err := p.GetKVNamespace(namespaceID); err != nil {
		return nil, false, err
	}
	var value []byte
	err := p.db.QueryRow(`SELECT value FROM runtime_kv WHERE kv_namespace_id = $1 AND key = $2`, namespaceID, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return value, err == nil, err
}

func (p *Postgres) KVList(capability, namespaceID string) ([]nanoflare.WorkerKVKey, error) {
	if _, err := p.AppIDForCapability(capability); err != nil {
		return nil, err
	}
	if _, err := p.GetKVNamespace(namespaceID); err != nil {
		return nil, err
	}
	rows, err := p.db.Query(`SELECT key, octet_length(value) FROM runtime_kv WHERE kv_namespace_id = $1 ORDER BY key`, namespaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []nanoflare.WorkerKVKey{}
	for rows.Next() {
		var item nanoflare.WorkerKVKey
		if err := rows.Scan(&item.Key, &item.Size); err != nil {
			return nil, err
		}
		keys = append(keys, item)
	}
	return keys, rows.Err()
}

func (p *Postgres) KVPut(capability, namespaceID, key string, value []byte) error {
	if _, err := p.AppIDForCapability(capability); err != nil {
		return err
	}
	if _, err := p.GetKVNamespace(namespaceID); err != nil {
		return err
	}
	_, err := p.db.Exec(`
INSERT INTO runtime_kv (kv_namespace_id, key, value) VALUES ($1, $2, $3)
ON CONFLICT (kv_namespace_id, key) DO UPDATE SET value = EXCLUDED.value`, namespaceID, key, value)
	return err
}

func randomToken() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func (p *Postgres) KVDelete(capability, namespaceID, key string) error {
	if _, err := p.AppIDForCapability(capability); err != nil {
		return err
	}
	if _, err := p.GetKVNamespace(namespaceID); err != nil {
		return err
	}
	_, err := p.db.Exec(`DELETE FROM runtime_kv WHERE kv_namespace_id = $1 AND key = $2`, namespaceID, key)
	return err
}

func (p *Postgres) KVNamespaceMetrics(namespaceID string) (nanoflare.KVNamespaceMetrics, error) {
	if _, err := p.GetKVNamespace(namespaceID); err != nil {
		return nanoflare.KVNamespaceMetrics{}, err
	}
	var metrics nanoflare.KVNamespaceMetrics
	err := p.db.QueryRow(`
SELECT reads, writes
FROM kv_namespace_metrics
WHERE kv_namespace_id = $1`, namespaceID).Scan(&metrics.Reads, &metrics.Writes)
	if errors.Is(err, sql.ErrNoRows) {
		metrics.Available = true
		return metrics, nil
	}
	metrics.Available = err == nil
	return metrics, err
}

func (p *Postgres) IncrementKVNamespaceReads(namespaceID string) error {
	if _, err := p.GetKVNamespace(namespaceID); err != nil {
		return err
	}
	_, err := p.db.Exec(`
INSERT INTO kv_namespace_metrics (kv_namespace_id, reads, writes) VALUES ($1, 1, 0)
ON CONFLICT (kv_namespace_id) DO UPDATE SET reads = kv_namespace_metrics.reads + 1`, namespaceID)
	return err
}

func (p *Postgres) IncrementKVNamespaceWrites(namespaceID string) error {
	if _, err := p.GetKVNamespace(namespaceID); err != nil {
		return err
	}
	_, err := p.db.Exec(`
INSERT INTO kv_namespace_metrics (kv_namespace_id, reads, writes) VALUES ($1, 0, 1)
ON CONFLICT (kv_namespace_id) DO UPDATE SET writes = kv_namespace_metrics.writes + 1`, namespaceID)
	return err
}

func (p *Postgres) ObjectStorageBucketMetrics(bucketID string) (nanoflare.ObjectStorageBucketMetrics, error) {
	if _, err := p.GetObjectStorageBucket(bucketID); err != nil {
		return nanoflare.ObjectStorageBucketMetrics{}, err
	}
	var metrics nanoflare.ObjectStorageBucketMetrics
	err := p.db.QueryRow(`
SELECT reads, writes, size
FROM object_storage_bucket_metrics
WHERE bucket_id = $1`, bucketID).Scan(&metrics.Reads, &metrics.Writes, &metrics.Size)
	if errors.Is(err, sql.ErrNoRows) {
		metrics.Available = true
		return metrics, nil
	}
	metrics.Available = err == nil
	return metrics, err
}

func (p *Postgres) IncrementObjectStorageBucketReads(bucketID string) error {
	if _, err := p.GetObjectStorageBucket(bucketID); err != nil {
		return err
	}
	_, err := p.db.Exec(`
INSERT INTO object_storage_bucket_metrics (bucket_id, reads, writes, size) VALUES ($1, 1, 0, 0)
ON CONFLICT (bucket_id) DO UPDATE SET reads = object_storage_bucket_metrics.reads + 1`, bucketID)
	return err
}

func (p *Postgres) IncrementObjectStorageBucketWrites(bucketID string) error {
	if _, err := p.GetObjectStorageBucket(bucketID); err != nil {
		return err
	}
	_, err := p.db.Exec(`
INSERT INTO object_storage_bucket_metrics (bucket_id, reads, writes, size) VALUES ($1, 0, 1, 0)
ON CONFLICT (bucket_id) DO UPDATE SET writes = object_storage_bucket_metrics.writes + 1`, bucketID)
	return err
}

func (p *Postgres) AdjustObjectStorageBucketSize(bucketID string, delta int64) error {
	if _, err := p.GetObjectStorageBucket(bucketID); err != nil {
		return err
	}
	_, err := p.db.Exec(`
INSERT INTO object_storage_bucket_metrics (bucket_id, reads, writes, size) VALUES ($1, 0, 0, GREATEST($2, 0))
ON CONFLICT (bucket_id) DO UPDATE SET size = GREATEST(object_storage_bucket_metrics.size + $2, 0)`, bucketID, delta)
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

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

func (p *Postgres) columnExists(ctx context.Context, table, column string) (bool, error) {
	var exists bool
	err := p.db.QueryRowContext(ctx, `
SELECT EXISTS (
	SELECT 1 FROM information_schema.columns
	WHERE table_schema = current_schema()
		AND table_name = $1
		AND column_name = $2
)`, table, column).Scan(&exists)
	return exists, err
}

func legacyKVNamespaceID(appID string) string {
	return "legacy-" + appID
}

func legacyKVNamespaceName(appID string) string {
	return "legacy-" + appID
}
