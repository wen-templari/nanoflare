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
	"time"

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
DO $$
BEGIN
	IF to_regclass('apps') IS NOT NULL AND to_regclass('workers') IS NULL THEN
		ALTER TABLE apps RENAME TO workers;
	END IF;
	IF to_regclass('app_secrets') IS NOT NULL AND to_regclass('worker_secrets') IS NULL THEN
		ALTER TABLE app_secrets RENAME TO worker_secrets;
	END IF;
	IF EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema()
			AND table_name = 'deployments'
			AND column_name = 'app_id'
	) AND NOT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema()
			AND table_name = 'deployments'
			AND column_name = 'worker_id'
	) THEN
		ALTER TABLE deployments RENAME COLUMN app_id TO worker_id;
	END IF;
	IF EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema()
			AND table_name = 'worker_secrets'
			AND column_name = 'app_id'
	) AND NOT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema()
			AND table_name = 'worker_secrets'
			AND column_name = 'worker_id'
	) THEN
		ALTER TABLE worker_secrets RENAME COLUMN app_id TO worker_id;
	END IF;
	IF EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema()
			AND table_name = 'runtime_kv'
			AND column_name = 'app_id'
	) AND NOT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema()
			AND table_name = 'runtime_kv'
			AND column_name = 'worker_id'
	) THEN
		ALTER TABLE runtime_kv RENAME COLUMN app_id TO worker_id;
	END IF;
END $$;
CREATE TABLE IF NOT EXISTS workers (
	id text PRIMARY KEY,
	org_id text NOT NULL DEFAULT '',
	name text NOT NULL,
	hostname text NOT NULL UNIQUE,
	auth jsonb NOT NULL DEFAULT '{}'::jsonb,
	external_id text NOT NULL DEFAULT '',
	oauth_client_id text NOT NULL DEFAULT '',
	created_by text NOT NULL DEFAULT '',
	runtime_token text,
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS users (
	id text PRIMARY KEY,
	email text NOT NULL UNIQUE,
	password_hash bytea NOT NULL,
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS user_oidc_identities (
	issuer text NOT NULL,
	subject text NOT NULL,
	user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at timestamptz NOT NULL,
	PRIMARY KEY (issuer, subject)
);
CREATE TABLE IF NOT EXISTS control_refresh_tokens (
	token_hash text PRIMARY KEY,
	user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at timestamptz NOT NULL,
	revoked_at timestamptz,
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS personal_access_tokens (
	id text PRIMARY KEY,
	token_hash text NOT NULL UNIQUE,
	name text NOT NULL,
	user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	org_id text NOT NULL DEFAULT '',
	scope_type text NOT NULL DEFAULT 'user',
	scopes jsonb NOT NULL DEFAULT '[]'::jsonb,
	expires_at timestamptz,
	last_used_at timestamptz,
	revoked_at timestamptz,
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS organizations (
	id text PRIMARY KEY,
	name text NOT NULL,
	usage_level text NOT NULL DEFAULT 'default',
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS user_organizations (
	user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	role text NOT NULL DEFAULT 'owner',
	scopes jsonb NOT NULL DEFAULT '[]'::jsonb,
	created_at timestamptz NOT NULL DEFAULT NOW(),
	PRIMARY KEY (user_id, organization_id)
);
CREATE TABLE IF NOT EXISTS organization_invites (
	id text PRIMARY KEY,
	token_hash text NOT NULL UNIQUE,
	org_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	email text NOT NULL,
	role text NOT NULL,
	scopes jsonb NOT NULL DEFAULT '[]'::jsonb,
	inviter_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at timestamptz NOT NULL,
	accepted_at timestamptz,
	revoked_at timestamptz,
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_clients (
	id text PRIMARY KEY,
	owner_org_id text NOT NULL DEFAULT '',
	name text NOT NULL,
	redirect_uris jsonb NOT NULL DEFAULT '[]'::jsonb,
	scopes jsonb NOT NULL DEFAULT '[]'::jsonb,
	secret_hash bytea NOT NULL,
	disabled boolean NOT NULL DEFAULT false,
	created_at timestamptz NOT NULL,
	updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_authorization_codes (
	code_hash text PRIMARY KEY,
	client_id text NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
	user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	org_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	redirect_uri text NOT NULL,
	scopes jsonb NOT NULL DEFAULT '[]'::jsonb,
	expires_at timestamptz NOT NULL,
	used_at timestamptz,
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_tokens (
	token_hash text PRIMARY KEY,
	refresh_token_hash text NOT NULL UNIQUE,
	client_id text NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
	user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	org_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	scopes jsonb NOT NULL DEFAULT '[]'::jsonb,
	expires_at timestamptz NOT NULL,
	refresh_expires_at timestamptz NOT NULL,
	revoked_at timestamptz,
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS deployments (
	id text PRIMARY KEY,
	worker_id text NOT NULL REFERENCES workers(id),
	commit_hash text NOT NULL DEFAULT '',
	commit_message text NOT NULL DEFAULT '',
	created_by text NOT NULL DEFAULT '',
	files jsonb NOT NULL,
	assets jsonb NOT NULL DEFAULT '[]',
	entrypoint text NOT NULL,
	format text NOT NULL DEFAULT '',
	compatibility_date text NOT NULL,
	compatibility_flags jsonb NOT NULL DEFAULT '[]'::jsonb,
	triggers jsonb NOT NULL DEFAULT '{}'::jsonb,
	vars jsonb NOT NULL DEFAULT '{}'::jsonb,
	kv_namespaces jsonb NOT NULL DEFAULT '[]'::jsonb,
	db jsonb NOT NULL DEFAULT '[]'::jsonb,
	object_storage_bucket jsonb NOT NULL DEFAULT '[]'::jsonb,
	asset_config jsonb NOT NULL DEFAULT '{}'::jsonb,
	bundle_size bigint NOT NULL DEFAULT 0,
	object_key text NOT NULL DEFAULT '',
	port integer NOT NULL,
	created_at timestamptz NOT NULL,
	active boolean NOT NULL DEFAULT false,
	traffic_percent integer NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS kv_namespaces (
	id text PRIMARY KEY,
	org_id text NOT NULL DEFAULT '',
	name text NOT NULL UNIQUE,
	external_id text NOT NULL DEFAULT '',
	oauth_client_id text NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS databases (
	id text PRIMARY KEY,
	org_id text NOT NULL DEFAULT '',
	name text NOT NULL,
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS object_storage_buckets (
	id text PRIMARY KEY,
	org_id text NOT NULL DEFAULT '',
	name text NOT NULL UNIQUE,
	external_id text NOT NULL DEFAULT '',
	oauth_client_id text NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS worker_secrets (
	worker_id text NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
	name text NOT NULL,
	nonce bytea NOT NULL,
	ciphertext bytea NOT NULL,
	created_at timestamptz NOT NULL,
	updated_at timestamptz NOT NULL,
	PRIMARY KEY (worker_id, name)
);
DROP INDEX IF EXISTS deployments_active_app_idx;
DROP INDEX IF EXISTS deployments_active_worker_idx;
ALTER TABLE workers ADD COLUMN IF NOT EXISTS name text NOT NULL DEFAULT '';
ALTER TABLE workers ADD COLUMN IF NOT EXISTS org_id text NOT NULL DEFAULT '';
ALTER TABLE workers ADD COLUMN IF NOT EXISTS auth jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE workers ADD COLUMN IF NOT EXISTS external_id text NOT NULL DEFAULT '';
ALTER TABLE workers ADD COLUMN IF NOT EXISTS oauth_client_id text NOT NULL DEFAULT '';
ALTER TABLE workers ADD COLUMN IF NOT EXISTS created_by text NOT NULL DEFAULT '';
ALTER TABLE workers ADD COLUMN IF NOT EXISTS runtime_token text;
ALTER TABLE organizations ADD COLUMN IF NOT EXISTS usage_level text NOT NULL DEFAULT 'default';
UPDATE organizations SET usage_level = 'default' WHERE usage_level = '';
ALTER TABLE oauth_clients ADD COLUMN IF NOT EXISTS owner_org_id text NOT NULL DEFAULT '';
ALTER TABLE user_organizations ADD COLUMN IF NOT EXISTS role text NOT NULL DEFAULT 'owner';
ALTER TABLE user_organizations ADD COLUMN IF NOT EXISTS scopes jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE personal_access_tokens ADD COLUMN IF NOT EXISTS scope_type text NOT NULL DEFAULT 'user';
ALTER TABLE personal_access_tokens ADD COLUMN IF NOT EXISTS scopes jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE personal_access_tokens ADD COLUMN IF NOT EXISTS last_used_at timestamptz;
UPDATE user_organizations SET role = 'owner' WHERE role = '';
UPDATE user_organizations SET scopes = '["workers:read","workers:write","deployments:write","secrets:write","kv:read","kv:write","objects:read","objects:write","orgs:read","members:read","members:write","orgs:write","members:owner"]'::jsonb WHERE scopes = '[]'::jsonb;
UPDATE user_organizations
SET scopes = (
	SELECT jsonb_agg(DISTINCT CASE scope
		WHEN 'apps:read' THEN 'workers:read'
		WHEN 'apps:write' THEN 'workers:write'
		ELSE scope
	END)
	FROM jsonb_array_elements_text(scopes) AS scope
)
WHERE scopes ? 'apps:read' OR scopes ? 'apps:write';
UPDATE personal_access_tokens
SET scopes = (
	SELECT jsonb_agg(DISTINCT CASE scope
		WHEN 'apps:read' THEN 'workers:read'
		WHEN 'apps:write' THEN 'workers:write'
		ELSE scope
	END)
	FROM jsonb_array_elements_text(scopes) AS scope
)
WHERE scopes ? 'apps:read' OR scopes ? 'apps:write';
UPDATE organization_invites
SET scopes = (
	SELECT jsonb_agg(DISTINCT CASE scope
		WHEN 'apps:read' THEN 'workers:read'
		WHEN 'apps:write' THEN 'workers:write'
		ELSE scope
	END)
	FROM jsonb_array_elements_text(scopes) AS scope
)
WHERE scopes ? 'apps:read' OR scopes ? 'apps:write';
UPDATE oauth_clients
SET scopes = (
	SELECT jsonb_agg(DISTINCT CASE scope
		WHEN 'apps:read' THEN 'workers:read'
		WHEN 'apps:write' THEN 'workers:write'
		ELSE scope
	END)
	FROM jsonb_array_elements_text(scopes) AS scope
)
WHERE scopes ? 'apps:read' OR scopes ? 'apps:write';
UPDATE oauth_authorization_codes
SET scopes = (
	SELECT jsonb_agg(DISTINCT CASE scope
		WHEN 'apps:read' THEN 'workers:read'
		WHEN 'apps:write' THEN 'workers:write'
		ELSE scope
	END)
	FROM jsonb_array_elements_text(scopes) AS scope
)
WHERE scopes ? 'apps:read' OR scopes ? 'apps:write';
UPDATE oauth_tokens
SET scopes = (
	SELECT jsonb_agg(DISTINCT CASE scope
		WHEN 'apps:read' THEN 'workers:read'
		WHEN 'apps:write' THEN 'workers:write'
		ELSE scope
	END)
	FROM jsonb_array_elements_text(scopes) AS scope
)
WHERE scopes ? 'apps:read' OR scopes ? 'apps:write';
UPDATE user_organizations
SET scopes = (
	SELECT jsonb_agg(DISTINCT scope)
	FROM jsonb_array_elements_text(scopes || '["db:read"]'::jsonb) AS scope
)
WHERE scopes ? 'kv:read' AND NOT scopes ? 'db:read';
UPDATE user_organizations
SET scopes = (
	SELECT jsonb_agg(DISTINCT scope)
	FROM jsonb_array_elements_text(scopes || '["db:write"]'::jsonb) AS scope
)
WHERE scopes ? 'kv:write' AND NOT scopes ? 'db:write';
UPDATE workers SET name = hostname WHERE name = '';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS files jsonb NOT NULL DEFAULT '[]';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS assets jsonb NOT NULL DEFAULT '[]';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS entrypoint text NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS format text NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS compatibility_flags jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS triggers jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS vars jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS kv_namespaces jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS db jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS object_storage_bucket jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS asset_config jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS bundle_size bigint NOT NULL DEFAULT 0;
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS object_key text NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS traffic_percent integer NOT NULL DEFAULT 0;
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS commit_hash text NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS commit_message text NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS created_by text NOT NULL DEFAULT '';
DROP INDEX IF EXISTS deployments_active_app_idx;
DROP INDEX IF EXISTS deployments_active_worker_idx;
UPDATE deployments SET traffic_percent = 100 WHERE active AND traffic_percent = 0;
ALTER TABLE kv_namespaces ADD COLUMN IF NOT EXISTS org_id text NOT NULL DEFAULT '';
ALTER TABLE kv_namespaces ADD COLUMN IF NOT EXISTS external_id text NOT NULL DEFAULT '';
ALTER TABLE kv_namespaces ADD COLUMN IF NOT EXISTS oauth_client_id text NOT NULL DEFAULT '';
ALTER TABLE object_storage_buckets ADD COLUMN IF NOT EXISTS org_id text NOT NULL DEFAULT '';
ALTER TABLE object_storage_buckets ADD COLUMN IF NOT EXISTS external_id text NOT NULL DEFAULT '';
ALTER TABLE object_storage_buckets ADD COLUMN IF NOT EXISTS oauth_client_id text NOT NULL DEFAULT '';
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
	writes bigint NOT NULL DEFAULT 0,
	size bigint NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS object_storage_bucket_metrics (
	bucket_id text PRIMARY KEY REFERENCES object_storage_buckets(id) ON DELETE CASCADE,
	reads bigint NOT NULL DEFAULT 0,
	writes bigint NOT NULL DEFAULT 0,
	size bigint NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS database_metrics (
	database_id text PRIMARY KEY REFERENCES databases(id) ON DELETE CASCADE,
	queries bigint NOT NULL DEFAULT 0,
	read_queries bigint NOT NULL DEFAULT 0,
	write_queries bigint NOT NULL DEFAULT 0,
	rows_read bigint NOT NULL DEFAULT 0,
	rows_returned bigint NOT NULL DEFAULT 0,
	rows_written bigint NOT NULL DEFAULT 0,
	storage_bytes bigint NOT NULL DEFAULT 0,
	table_count bigint NOT NULL DEFAULT 0,
	total_duration_ms double precision NOT NULL DEFAULT 0,
	duration_bucket_0_5 bigint NOT NULL DEFAULT 0,
	duration_bucket_1 bigint NOT NULL DEFAULT 0,
	duration_bucket_2_5 bigint NOT NULL DEFAULT 0,
	duration_bucket_5 bigint NOT NULL DEFAULT 0,
	duration_bucket_10 bigint NOT NULL DEFAULT 0,
	duration_bucket_25 bigint NOT NULL DEFAULT 0,
	duration_bucket_50 bigint NOT NULL DEFAULT 0,
	duration_bucket_100 bigint NOT NULL DEFAULT 0,
	duration_bucket_250 bigint NOT NULL DEFAULT 0,
	duration_bucket_500 bigint NOT NULL DEFAULT 0,
	duration_bucket_1000 bigint NOT NULL DEFAULT 0,
	duration_bucket_inf bigint NOT NULL DEFAULT 0
);
ALTER TABLE kv_namespace_metrics ADD COLUMN IF NOT EXISTS size bigint NOT NULL DEFAULT 0;
INSERT INTO kv_namespace_metrics (kv_namespace_id, reads, writes, size)
SELECT kv_namespace_id, 0, 0, COALESCE(SUM(octet_length(value)), 0)
FROM runtime_kv
GROUP BY kv_namespace_id
ON CONFLICT (kv_namespace_id) DO UPDATE SET size = EXCLUDED.size
WHERE kv_namespace_metrics.size = 0;
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
CREATE UNIQUE INDEX IF NOT EXISTS databases_org_name_idx ON databases(org_id, name);
CREATE UNIQUE INDEX IF NOT EXISTS object_storage_buckets_org_name_idx ON object_storage_buckets(org_id, name);
ALTER TABLE workers ALTER COLUMN runtime_token SET NOT NULL;
DROP INDEX IF EXISTS apps_runtime_token_idx;
CREATE UNIQUE INDEX IF NOT EXISTS workers_runtime_token_idx ON workers(runtime_token);
ALTER TABLE deployments DROP COLUMN IF EXISTS bundle_path;
ALTER TABLE deployments DROP COLUMN IF EXISTS capability_token;`)
	return err
}

func (p *Postgres) migrateKVNamespaces(ctx context.Context) error {
	hasAppID, err := p.columnExists(ctx, "runtime_kv", "worker_id")
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
	rows, err := tx.QueryContext(ctx, `SELECT id FROM workers ORDER BY id`)
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
WHERE worker_id = $2 AND (kv_namespace_id IS NULL OR kv_namespace_id = '')`,
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
		if _, err := tx.ExecContext(ctx, `ALTER TABLE runtime_kv DROP COLUMN worker_id`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (p *Postgres) migrateRuntimeTokens(ctx context.Context) error {
	rows, err := p.db.QueryContext(ctx, `SELECT id FROM workers WHERE runtime_token IS NULL OR runtime_token = ''`)
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
		if _, err := p.db.ExecContext(ctx, `UPDATE workers SET runtime_token = $1 WHERE id = $2`, token, id); err != nil {
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

func (p *Postgres) UserByID(userID string) (nanoflare.User, error) {
	var user nanoflare.User
	err := p.db.QueryRow(`SELECT id, email, password_hash, created_at FROM users WHERE id = $1`, userID).
		Scan(&user.ID, &user.Email, &user.PasswordHash, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.User{}, nanoflare.ErrUserNotFound
	}
	return user, err
}

func (p *Postgres) UserByOIDCIdentity(issuer, subject string) (nanoflare.User, error) {
	var user nanoflare.User
	err := p.db.QueryRow(`
SELECT u.id, u.email, u.password_hash, u.created_at
FROM user_oidc_identities i
JOIN users u ON u.id = i.user_id
WHERE i.issuer = $1 AND i.subject = $2`, issuer, subject).
		Scan(&user.ID, &user.Email, &user.PasswordHash, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.User{}, nanoflare.ErrOIDCIdentityNotFound
	}
	return user, err
}

func (p *Postgres) CreateOIDCIdentity(identity nanoflare.UserOIDCIdentity) error {
	_, err := p.db.Exec(`
INSERT INTO user_oidc_identities (issuer, subject, user_id, created_at)
VALUES ($1, $2, $3, $4)`,
		identity.Issuer, identity.Subject, identity.UserID, identity.CreatedAt)
	if isUniqueViolation(err) {
		return nanoflare.ErrOIDCIdentityExists
	}
	if isForeignKeyViolation(err) {
		return nanoflare.ErrUserNotFound
	}
	return err
}

func (p *Postgres) CreateControlRefreshToken(token nanoflare.ControlRefreshToken) error {
	_, err := p.db.Exec(`
INSERT INTO control_refresh_tokens (token_hash, user_id, expires_at, revoked_at, created_at)
VALUES ($1, $2, $3, $4, $5)`,
		token.TokenHash, token.UserID, token.ExpiresAt, token.RevokedAt, token.CreatedAt)
	if isForeignKeyViolation(err) {
		return nanoflare.ErrUserNotFound
	}
	return err
}

func (p *Postgres) ControlRefreshToken(tokenHash string) (nanoflare.ControlRefreshToken, error) {
	var token nanoflare.ControlRefreshToken
	err := p.db.QueryRow(`
SELECT token_hash, user_id, expires_at, revoked_at, created_at
FROM control_refresh_tokens WHERE token_hash = $1`, tokenHash).
		Scan(&token.TokenHash, &token.UserID, &token.ExpiresAt, &token.RevokedAt, &token.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.ControlRefreshToken{}, nanoflare.ErrControlRefreshTokenNotFound
	}
	return token, err
}

func (p *Postgres) UpdateControlRefreshToken(token nanoflare.ControlRefreshToken) error {
	result, err := p.db.Exec(`UPDATE control_refresh_tokens SET revoked_at = $2 WHERE token_hash = $1 AND revoked_at IS NULL`, token.TokenHash, token.RevokedAt)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return nanoflare.ErrControlRefreshTokenNotFound
	}
	return nil
}

func (p *Postgres) CreatePersonalAccessToken(token nanoflare.PersonalAccessToken) error {
	_, err := p.db.Exec(`
INSERT INTO personal_access_tokens (id, token_hash, name, user_id, org_id, scope_type, scopes, expires_at, last_used_at, revoked_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		token.ID, token.TokenHash, token.Name, token.UserID, token.OrgID, token.ScopeType, mustJSON(token.Scopes), token.ExpiresAt, token.LastUsedAt, token.RevokedAt, token.CreatedAt)
	if isForeignKeyViolation(err) {
		return nanoflare.ErrUserNotFound
	}
	if isUniqueViolation(err) {
		return nanoflare.ErrPersonalAccessTokenNotFound
	}
	return err
}

func (p *Postgres) PersonalAccessTokenByHash(tokenHash string) (nanoflare.PersonalAccessToken, error) {
	return p.personalAccessToken(`token_hash = $1`, tokenHash)
}

func (p *Postgres) PersonalAccessTokensByUser(userID string) ([]nanoflare.PersonalAccessToken, error) {
	rows, err := p.db.Query(`
SELECT id, token_hash, name, user_id, org_id, scope_type, scopes, expires_at, last_used_at, revoked_at, created_at
FROM personal_access_tokens
WHERE user_id = $1
ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []nanoflare.PersonalAccessToken
	for rows.Next() {
		token, err := scanPersonalAccessToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func (p *Postgres) UpdatePersonalAccessToken(token nanoflare.PersonalAccessToken) error {
	result, err := p.db.Exec(`
UPDATE personal_access_tokens
SET token_hash = $2, name = $3, user_id = $4, org_id = $5, scope_type = $6, scopes = $7, expires_at = $8, last_used_at = $9, revoked_at = $10, created_at = $11
WHERE id = $1`,
		token.ID, token.TokenHash, token.Name, token.UserID, token.OrgID, token.ScopeType, mustJSON(token.Scopes), token.ExpiresAt, token.LastUsedAt, token.RevokedAt, token.CreatedAt)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return nanoflare.ErrPersonalAccessTokenNotFound
	}
	return nil
}

func (p *Postgres) personalAccessToken(where, value string) (nanoflare.PersonalAccessToken, error) {
	row := p.db.QueryRow(`
SELECT id, token_hash, name, user_id, org_id, scope_type, scopes, expires_at, last_used_at, revoked_at, created_at
FROM personal_access_tokens WHERE `+where, value)
	token, err := scanPersonalAccessToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.PersonalAccessToken{}, nanoflare.ErrPersonalAccessTokenNotFound
	}
	return token, err
}

func (p *Postgres) UserCount() (int, error) {
	var count int
	err := p.db.QueryRow(`SELECT count(*) FROM users`).Scan(&count)
	return count, err
}

func (p *Postgres) CreateOrganization(org nanoflare.Organization) error {
	_, err := p.db.Exec(`INSERT INTO organizations (id, name, usage_level, created_at) VALUES ($1, $2, $3, $4)`,
		org.ID, org.Name, nanoflare.NormalizeUsageLevel(org.UsageLevel), org.CreatedAt)
	if isUniqueViolation(err) {
		return nanoflare.ErrOrganizationExists
	}
	return err
}

func (p *Postgres) GetOrganization(orgID string) (nanoflare.Organization, error) {
	var org nanoflare.Organization
	err := p.db.QueryRow(`SELECT id, name, usage_level, created_at FROM organizations WHERE id = $1`, orgID).Scan(&org.ID, &org.Name, &org.UsageLevel, &org.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.Organization{}, nanoflare.ErrOrganizationNotFound
	}
	org.UsageLevel = nanoflare.NormalizeUsageLevel(org.UsageLevel)
	return org, err
}

func (p *Postgres) CountOwnedOrganizationsByUser(userID string) (int, error) {
	var exists bool
	if err := p.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, userID).Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, nanoflare.ErrUserNotFound
	}
	var count int
	err := p.db.QueryRow(`SELECT count(*) FROM user_organizations WHERE user_id = $1 AND role = 'owner'`, userID).Scan(&count)
	return count, err
}

func (p *Postgres) UpsertOrganizationMembership(membership nanoflare.OrganizationMembership) error {
	_, err := p.db.Exec(`
INSERT INTO user_organizations (user_id, organization_id, role, scopes, created_at)
VALUES ($1, $2, $3, $4, COALESCE(NULLIF($5, '0001-01-01T00:00:00Z')::timestamptz, NOW()))
ON CONFLICT (user_id, organization_id) DO UPDATE SET role = EXCLUDED.role, scopes = EXCLUDED.scopes`,
		membership.UserID, membership.OrgID, membership.Role, mustJSON(membership.Scopes), membership.CreatedAt.Format(time.RFC3339))
	if isForeignKeyViolation(err) {
		return nanoflare.ErrMembershipNotFound
	}
	return err
}

func (p *Postgres) AddUserToOrganization(userID, orgID string) error {
	return p.UpsertOrganizationMembership(nanoflare.OrganizationMembership{
		UserID: userID,
		OrgID:  orgID,
		Role:   nanoflare.RoleOwner,
		Scopes: nanoflare.RoleScopes(nanoflare.RoleOwner),
	})
}

func (p *Postgres) OrganizationMembership(userID, orgID string) (nanoflare.OrganizationMembership, error) {
	var membership nanoflare.OrganizationMembership
	var scopes []byte
	err := p.db.QueryRow(`
SELECT uo.user_id, u.email, uo.organization_id, uo.role, uo.scopes, uo.created_at
FROM user_organizations uo
JOIN users u ON u.id = uo.user_id
WHERE uo.user_id = $1 AND uo.organization_id = $2`, userID, orgID).
		Scan(&membership.UserID, &membership.UserEmail, &membership.OrgID, &membership.Role, &scopes, &membership.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.OrganizationMembership{}, nanoflare.ErrMembershipNotFound
	}
	if err != nil {
		return nanoflare.OrganizationMembership{}, err
	}
	if err := json.Unmarshal(scopes, &membership.Scopes); err != nil {
		return nanoflare.OrganizationMembership{}, err
	}
	return membership, nil
}

func (p *Postgres) ListOrganizationMembers(orgID string) ([]nanoflare.OrganizationMembership, error) {
	rows, err := p.db.Query(`
SELECT uo.user_id, u.email, uo.organization_id, uo.role, uo.scopes, uo.created_at
FROM user_organizations uo
JOIN users u ON u.id = uo.user_id
WHERE uo.organization_id = $1
ORDER BY u.email`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []nanoflare.OrganizationMembership{}
	for rows.Next() {
		var member nanoflare.OrganizationMembership
		var scopes []byte
		if err := rows.Scan(&member.UserID, &member.UserEmail, &member.OrgID, &member.Role, &scopes, &member.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(scopes, &member.Scopes); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

func (p *Postgres) OwnerCount(orgID string) (int, error) {
	var count int
	err := p.db.QueryRow(`SELECT count(*) FROM user_organizations WHERE organization_id = $1 AND role = 'owner'`, orgID).Scan(&count)
	return count, err
}

func (p *Postgres) DeleteOrganizationMembership(userID, orgID string) error {
	result, err := p.db.Exec(`DELETE FROM user_organizations WHERE user_id = $1 AND organization_id = $2`, userID, orgID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return nanoflare.ErrMembershipNotFound
	}
	return nil
}

func (p *Postgres) ListOrganizationsForUser(userID string) ([]nanoflare.Organization, error) {
	rows, err := p.db.Query(`
SELECT o.id, o.name, o.usage_level, uo.role, uo.scopes, o.created_at
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
		var scopes []byte
		if err := rows.Scan(&org.ID, &org.Name, &org.UsageLevel, &org.Role, &scopes, &org.CreatedAt); err != nil {
			return nil, err
		}
		org.UsageLevel = nanoflare.NormalizeUsageLevel(org.UsageLevel)
		if err := json.Unmarshal(scopes, &org.Scopes); err != nil {
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

func (p *Postgres) CreateOrganizationInvite(invite nanoflare.OrganizationInvite) error {
	_, err := p.db.Exec(`
INSERT INTO organization_invites (id, token_hash, org_id, email, role, scopes, inviter_id, expires_at, accepted_at, revoked_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		invite.ID, invite.TokenHash, invite.OrgID, invite.Email, invite.Role, mustJSON(invite.Scopes), invite.InviterID, invite.ExpiresAt, invite.AcceptedAt, invite.RevokedAt, invite.CreatedAt)
	return err
}

func (p *Postgres) OrganizationInviteByTokenHash(tokenHash string) (nanoflare.OrganizationInvite, error) {
	return p.organizationInvite(`i.token_hash = $1`, tokenHash)
}

func (p *Postgres) OrganizationInviteByID(orgID, inviteID string) (nanoflare.OrganizationInvite, error) {
	row := p.db.QueryRow(`
SELECT i.id, i.token_hash, i.org_id, o.name, i.email, i.role, i.scopes, i.inviter_id, u.email, i.expires_at, i.accepted_at, i.revoked_at, i.created_at
FROM organization_invites i
JOIN organizations o ON o.id = i.org_id
JOIN users u ON u.id = i.inviter_id
WHERE i.org_id = $1 AND i.id = $2`, orgID, inviteID)
	invite, err := scanOrganizationInvite(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.OrganizationInvite{}, nanoflare.ErrInviteNotFound
	}
	return invite, err
}

func (p *Postgres) organizationInvite(where, arg string) (nanoflare.OrganizationInvite, error) {
	row := p.db.QueryRow(`
SELECT i.id, i.token_hash, i.org_id, o.name, i.email, i.role, i.scopes, i.inviter_id, u.email, i.expires_at, i.accepted_at, i.revoked_at, i.created_at
FROM organization_invites i
JOIN organizations o ON o.id = i.org_id
JOIN users u ON u.id = i.inviter_id
WHERE `+where, arg)
	invite, err := scanOrganizationInvite(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.OrganizationInvite{}, nanoflare.ErrInviteNotFound
	}
	return invite, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanOrganizationInvite(row scanner) (nanoflare.OrganizationInvite, error) {
	var invite nanoflare.OrganizationInvite
	var scopes []byte
	err := row.Scan(&invite.ID, &invite.TokenHash, &invite.OrgID, &invite.OrgName, &invite.Email, &invite.Role, &scopes, &invite.InviterID, &invite.InviterEmail, &invite.ExpiresAt, &invite.AcceptedAt, &invite.RevokedAt, &invite.CreatedAt)
	if err != nil {
		return nanoflare.OrganizationInvite{}, err
	}
	if err := json.Unmarshal(scopes, &invite.Scopes); err != nil {
		return nanoflare.OrganizationInvite{}, err
	}
	return invite, nil
}

func scanPersonalAccessToken(row scanner) (nanoflare.PersonalAccessToken, error) {
	var token nanoflare.PersonalAccessToken
	var scopes []byte
	err := row.Scan(&token.ID, &token.TokenHash, &token.Name, &token.UserID, &token.OrgID, &token.ScopeType, &scopes, &token.ExpiresAt, &token.LastUsedAt, &token.RevokedAt, &token.CreatedAt)
	if err != nil {
		return nanoflare.PersonalAccessToken{}, err
	}
	if err := json.Unmarshal(scopes, &token.Scopes); err != nil {
		return nanoflare.PersonalAccessToken{}, err
	}
	return token, nil
}

func (p *Postgres) OrganizationInvitesByOrg(orgID string) ([]nanoflare.OrganizationInvite, error) {
	rows, err := p.db.Query(`
SELECT i.id, i.token_hash, i.org_id, o.name, i.email, i.role, i.scopes, i.inviter_id, u.email, i.expires_at, i.accepted_at, i.revoked_at, i.created_at
FROM organization_invites i
JOIN organizations o ON o.id = i.org_id
JOIN users u ON u.id = i.inviter_id
WHERE i.org_id = $1
ORDER BY i.created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	invites := []nanoflare.OrganizationInvite{}
	for rows.Next() {
		invite, err := scanOrganizationInvite(rows)
		if err != nil {
			return nil, err
		}
		invites = append(invites, invite)
	}
	return invites, rows.Err()
}

func (p *Postgres) UpdateOrganizationInvite(invite nanoflare.OrganizationInvite) error {
	result, err := p.db.Exec(`UPDATE organization_invites SET accepted_at = $2, revoked_at = $3 WHERE token_hash = $1`, invite.TokenHash, invite.AcceptedAt, invite.RevokedAt)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return nanoflare.ErrInviteNotFound
	}
	return nil
}

func (p *Postgres) CreateOAuthClient(client nanoflare.OAuthClient) error {
	_, err := p.db.Exec(`
INSERT INTO oauth_clients (id, owner_org_id, name, redirect_uris, scopes, secret_hash, disabled, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		client.ID, client.OwnerOrgID, client.Name, mustJSON(client.RedirectURIs), mustJSON(client.Scopes), client.SecretHash, client.Disabled, client.CreatedAt, client.UpdatedAt)
	return err
}

func (p *Postgres) CountOAuthClientsByOwnerOrg(ownerOrgID string) (int, error) {
	var count int
	err := p.db.QueryRow(`SELECT count(*) FROM oauth_clients WHERE owner_org_id = $1`, ownerOrgID).Scan(&count)
	return count, err
}

func (p *Postgres) OAuthClient(clientID string) (nanoflare.OAuthClient, error) {
	var client nanoflare.OAuthClient
	var redirectURIs, scopes []byte
	err := p.db.QueryRow(`
SELECT id, owner_org_id, name, redirect_uris, scopes, secret_hash, disabled, created_at, updated_at
FROM oauth_clients WHERE id = $1`, clientID).
		Scan(&client.ID, &client.OwnerOrgID, &client.Name, &redirectURIs, &scopes, &client.SecretHash, &client.Disabled, &client.CreatedAt, &client.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.OAuthClient{}, nanoflare.ErrOAuthClientNotFound
	}
	if err != nil {
		return nanoflare.OAuthClient{}, err
	}
	if err := json.Unmarshal(redirectURIs, &client.RedirectURIs); err != nil {
		return nanoflare.OAuthClient{}, err
	}
	if err := json.Unmarshal(scopes, &client.Scopes); err != nil {
		return nanoflare.OAuthClient{}, err
	}
	return client, nil
}

func (p *Postgres) OAuthClientsByOwnerOrg(ownerOrgID string) ([]nanoflare.OAuthClient, error) {
	rows, err := p.db.Query(`
SELECT id, owner_org_id, name, redirect_uris, scopes, secret_hash, disabled, created_at, updated_at
FROM oauth_clients
WHERE owner_org_id = $1
ORDER BY name, id`, ownerOrgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	clients := make([]nanoflare.OAuthClient, 0)
	for rows.Next() {
		var client nanoflare.OAuthClient
		var redirectURIs, scopes []byte
		if err := rows.Scan(&client.ID, &client.OwnerOrgID, &client.Name, &redirectURIs, &scopes, &client.SecretHash, &client.Disabled, &client.CreatedAt, &client.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(redirectURIs, &client.RedirectURIs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(scopes, &client.Scopes); err != nil {
			return nil, err
		}
		clients = append(clients, client)
	}
	return clients, rows.Err()
}

func (p *Postgres) UpdateOAuthClient(client nanoflare.OAuthClient) error {
	result, err := p.db.Exec(`
UPDATE oauth_clients
SET owner_org_id = $2, name = $3, redirect_uris = $4, scopes = $5, secret_hash = $6, disabled = $7, updated_at = $8
WHERE id = $1`,
		client.ID, client.OwnerOrgID, client.Name, mustJSON(client.RedirectURIs), mustJSON(client.Scopes), client.SecretHash, client.Disabled, client.UpdatedAt)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return nanoflare.ErrOAuthClientNotFound
	}
	return nil
}

func (p *Postgres) CreateOAuthAuthorizationCode(code nanoflare.OAuthAuthorizationCode) error {
	_, err := p.db.Exec(`
INSERT INTO oauth_authorization_codes (code_hash, client_id, user_id, org_id, redirect_uri, scopes, expires_at, used_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		code.CodeHash, code.ClientID, code.UserID, code.OrgID, code.RedirectURI, mustJSON(code.Scopes), code.ExpiresAt, code.UsedAt, code.CreatedAt)
	return err
}

func (p *Postgres) OAuthAuthorizationCode(codeHash string) (nanoflare.OAuthAuthorizationCode, error) {
	var code nanoflare.OAuthAuthorizationCode
	var scopes []byte
	err := p.db.QueryRow(`
SELECT code_hash, client_id, user_id, org_id, redirect_uri, scopes, expires_at, used_at, created_at
FROM oauth_authorization_codes WHERE code_hash = $1`, codeHash).
		Scan(&code.CodeHash, &code.ClientID, &code.UserID, &code.OrgID, &code.RedirectURI, &scopes, &code.ExpiresAt, &code.UsedAt, &code.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.OAuthAuthorizationCode{}, nanoflare.ErrOAuthInvalidGrant
	}
	if err != nil {
		return nanoflare.OAuthAuthorizationCode{}, err
	}
	if err := json.Unmarshal(scopes, &code.Scopes); err != nil {
		return nanoflare.OAuthAuthorizationCode{}, err
	}
	return code, nil
}

func (p *Postgres) UpdateOAuthAuthorizationCode(code nanoflare.OAuthAuthorizationCode) error {
	result, err := p.db.Exec(`UPDATE oauth_authorization_codes SET used_at = $2 WHERE code_hash = $1`, code.CodeHash, code.UsedAt)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return nanoflare.ErrOAuthInvalidGrant
	}
	return nil
}

func (p *Postgres) CreateOAuthToken(token nanoflare.OAuthToken) error {
	_, err := p.db.Exec(`
INSERT INTO oauth_tokens (token_hash, refresh_token_hash, client_id, user_id, org_id, scopes, expires_at, refresh_expires_at, revoked_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		token.TokenHash, token.RefreshTokenHash, token.ClientID, token.UserID, token.OrgID, mustJSON(token.Scopes), token.ExpiresAt, token.RefreshExpiresAt, token.RevokedAt, token.CreatedAt)
	return err
}

func (p *Postgres) OAuthAccessToken(tokenHash string) (nanoflare.OAuthToken, error) {
	return p.oauthToken(`token_hash = $1`, tokenHash)
}

func (p *Postgres) OAuthRefreshToken(refreshTokenHash string) (nanoflare.OAuthToken, error) {
	return p.oauthToken(`refresh_token_hash = $1`, refreshTokenHash)
}

func (p *Postgres) UpdateOAuthToken(token nanoflare.OAuthToken) error {
	result, err := p.db.Exec(`UPDATE oauth_tokens SET revoked_at = $2 WHERE token_hash = $1`, token.TokenHash, token.RevokedAt)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return nanoflare.ErrOAuthTokenNotFound
	}
	return nil
}

func (p *Postgres) OAuthConnections(userID, orgID string) ([]nanoflare.OAuthConnection, error) {
	rows, err := p.db.Query(`
SELECT DISTINCT ON (t.client_id) t.client_id, c.name, t.scopes, t.created_at
FROM oauth_tokens t
JOIN oauth_clients c ON c.id = t.client_id
WHERE t.user_id = $1 AND t.org_id = $2 AND t.revoked_at IS NULL
ORDER BY t.client_id, t.created_at`, userID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	connections := make([]nanoflare.OAuthConnection, 0)
	for rows.Next() {
		var connection nanoflare.OAuthConnection
		var scopes []byte
		if err := rows.Scan(&connection.ClientID, &connection.Name, &scopes, &connection.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(scopes, &connection.Scopes); err != nil {
			return nil, err
		}
		connections = append(connections, connection)
	}
	return connections, rows.Err()
}

func (p *Postgres) OAuthClientConnections(clientID string) ([]nanoflare.OAuthClientConnection, error) {
	rows, err := p.db.Query(`
SELECT DISTINCT ON (t.user_id, t.org_id)
	t.client_id, t.user_id, u.email, t.org_id, o.name, t.scopes, t.created_at
FROM oauth_tokens t
JOIN users u ON u.id = t.user_id
JOIN organizations o ON o.id = t.org_id
WHERE t.client_id = $1 AND t.revoked_at IS NULL
ORDER BY t.user_id, t.org_id, t.created_at`, clientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	connections := make([]nanoflare.OAuthClientConnection, 0)
	for rows.Next() {
		var connection nanoflare.OAuthClientConnection
		var scopes []byte
		if err := rows.Scan(&connection.ClientID, &connection.UserID, &connection.UserEmail, &connection.OrgID, &connection.OrgName, &scopes, &connection.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(scopes, &connection.Scopes); err != nil {
			return nil, err
		}
		connections = append(connections, connection)
	}
	return connections, rows.Err()
}

func (p *Postgres) RevokeOAuthClientTokens(userID, orgID, clientID string, revokedAt time.Time) error {
	_, err := p.db.Exec(`
UPDATE oauth_tokens
SET revoked_at = $4
WHERE user_id = $1 AND org_id = $2 AND client_id = $3 AND revoked_at IS NULL`, userID, orgID, clientID, revokedAt)
	return err
}

func (p *Postgres) RevokeAllOAuthClientTokens(clientID string, revokedAt time.Time) error {
	_, err := p.db.Exec(`
UPDATE oauth_tokens
SET revoked_at = $2
WHERE client_id = $1 AND revoked_at IS NULL`, clientID, revokedAt)
	return err
}

func (p *Postgres) oauthToken(where, value string) (nanoflare.OAuthToken, error) {
	var token nanoflare.OAuthToken
	var scopes []byte
	err := p.db.QueryRow(`
SELECT token_hash, refresh_token_hash, client_id, user_id, org_id, scopes, expires_at, refresh_expires_at, revoked_at, created_at
FROM oauth_tokens WHERE `+where, value).
		Scan(&token.TokenHash, &token.RefreshTokenHash, &token.ClientID, &token.UserID, &token.OrgID, &scopes, &token.ExpiresAt, &token.RefreshExpiresAt, &token.RevokedAt, &token.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.OAuthToken{}, nanoflare.ErrOAuthTokenNotFound
	}
	if err != nil {
		return nanoflare.OAuthToken{}, err
	}
	if err := json.Unmarshal(scopes, &token.Scopes); err != nil {
		return nanoflare.OAuthToken{}, err
	}
	return token, nil
}

func (p *Postgres) CreateApp(app nanoflare.App) error {
	_, err := p.db.Exec(`INSERT INTO workers (id, org_id, name, hostname, auth, external_id, oauth_client_id, created_by, runtime_token, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		app.ID, app.OrgID, app.Name, app.Hostname, mustJSON(app.Auth), app.ExternalID, app.OAuthClientID, app.CreatedBy, app.RuntimeToken, app.CreatedAt)
	if isUniqueViolation(err) {
		return nanoflare.ErrAppExists
	}
	return err
}

func (p *Postgres) CountAppsByOrg(orgID string) (int, error) {
	var count int
	err := p.db.QueryRow(`SELECT count(*) FROM workers WHERE org_id = $1`, orgID).Scan(&count)
	return count, err
}

func (p *Postgres) CreateKVNamespace(namespace nanoflare.KVNamespace) error {
	_, err := p.db.Exec(`INSERT INTO kv_namespaces (id, org_id, name, external_id, oauth_client_id, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		namespace.ID, namespace.OrgID, namespace.Name, namespace.ExternalID, namespace.OAuthClientID, namespace.CreatedAt)
	if isUniqueViolation(err) {
		return nanoflare.ErrKVNamespaceExists
	}
	return err
}

func (p *Postgres) CountKVNamespacesByOrg(orgID string) (int, error) {
	var count int
	err := p.db.QueryRow(`SELECT count(*) FROM kv_namespaces WHERE org_id = $1`, orgID).Scan(&count)
	return count, err
}

func (p *Postgres) ListKVNamespaces() ([]nanoflare.KVNamespace, error) {
	return p.ListKVNamespacesByOrg("")
}

func (p *Postgres) ListKVNamespacesByOrg(orgID string) ([]nanoflare.KVNamespace, error) {
	query := `SELECT id, org_id, name, external_id, oauth_client_id, created_at FROM kv_namespaces`
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
		if err := rows.Scan(&namespace.ID, &namespace.OrgID, &namespace.Name, &namespace.ExternalID, &namespace.OAuthClientID, &namespace.CreatedAt); err != nil {
			return nil, err
		}
		namespaces = append(namespaces, namespace)
	}
	return namespaces, rows.Err()
}

func (p *Postgres) GetKVNamespace(namespaceID string) (nanoflare.KVNamespace, error) {
	var namespace nanoflare.KVNamespace
	err := p.db.QueryRow(`SELECT id, org_id, name, external_id, oauth_client_id, created_at FROM kv_namespaces WHERE id = $1`, namespaceID).
		Scan(&namespace.ID, &namespace.OrgID, &namespace.Name, &namespace.ExternalID, &namespace.OAuthClientID, &namespace.CreatedAt)
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

func (p *Postgres) CreateDatabase(database nanoflare.Database) error {
	_, err := p.db.Exec(`INSERT INTO databases (id, org_id, name, created_at) VALUES ($1, $2, $3, $4)`,
		database.ID, database.OrgID, database.Name, database.CreatedAt)
	if isUniqueViolation(err) {
		return nanoflare.ErrDatabaseExists
	}
	return err
}

func (p *Postgres) CountDatabasesByOrg(orgID string) (int, error) {
	var count int
	err := p.db.QueryRow(`SELECT count(*) FROM databases WHERE org_id = $1`, orgID).Scan(&count)
	return count, err
}

func (p *Postgres) ListDatabases() ([]nanoflare.Database, error) {
	return p.ListDatabasesByOrg("")
}

func (p *Postgres) ListDatabasesByOrg(orgID string) ([]nanoflare.Database, error) {
	query := `SELECT id, org_id, name, created_at FROM databases`
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
	databases := make([]nanoflare.Database, 0)
	for rows.Next() {
		var database nanoflare.Database
		if err := rows.Scan(&database.ID, &database.OrgID, &database.Name, &database.CreatedAt); err != nil {
			return nil, err
		}
		databases = append(databases, database)
	}
	return databases, rows.Err()
}

func (p *Postgres) GetDatabase(databaseID string) (nanoflare.Database, error) {
	var database nanoflare.Database
	err := p.db.QueryRow(`SELECT id, org_id, name, created_at FROM databases WHERE id = $1`, databaseID).
		Scan(&database.ID, &database.OrgID, &database.Name, &database.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nanoflare.Database{}, nanoflare.ErrDatabaseNotFound
	}
	return database, err
}

func (p *Postgres) DeleteDatabase(databaseID string) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var inUse bool
	if err := tx.QueryRow(`
SELECT EXISTS (
	SELECT 1
	FROM deployments d, jsonb_array_elements(d.db) AS binding
	WHERE binding->>'database_id' = $1
)`, databaseID).Scan(&inUse); err != nil {
		return err
	}
	if inUse {
		return nanoflare.ErrDatabaseInUse
	}
	result, err := tx.Exec(`DELETE FROM databases WHERE id = $1`, databaseID)
	if err != nil {
		return err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if deleted == 0 {
		return nanoflare.ErrDatabaseNotFound
	}
	return tx.Commit()
}

func (p *Postgres) DatabaseMetrics(databaseID string) (nanoflare.DatabaseMetrics, error) {
	if _, err := p.GetDatabase(databaseID); err != nil {
		return nanoflare.DatabaseMetrics{}, err
	}
	var metrics nanoflare.DatabaseMetrics
	err := p.db.QueryRow(`
SELECT queries, read_queries, write_queries, rows_read, rows_returned, rows_written,
	storage_bytes, table_count, total_duration_ms,
	duration_bucket_0_5, duration_bucket_1, duration_bucket_2_5, duration_bucket_5,
	duration_bucket_10, duration_bucket_25, duration_bucket_50, duration_bucket_100,
	duration_bucket_250, duration_bucket_500, duration_bucket_1000, duration_bucket_inf
FROM database_metrics
WHERE database_id = $1`, databaseID).Scan(
		&metrics.Queries, &metrics.ReadQueries, &metrics.WriteQueries, &metrics.RowsRead, &metrics.RowsReturned, &metrics.RowsWritten,
		&metrics.StorageBytes, &metrics.TableCount, &metrics.TotalDurationMS,
		&metrics.DurationBucket0_5, &metrics.DurationBucket1, &metrics.DurationBucket2_5, &metrics.DurationBucket5,
		&metrics.DurationBucket10, &metrics.DurationBucket25, &metrics.DurationBucket50, &metrics.DurationBucket100,
		&metrics.DurationBucket250, &metrics.DurationBucket500, &metrics.DurationBucket1000, &metrics.DurationBucketInf,
	)
	if errors.Is(err, sql.ErrNoRows) {
		metrics.Available = true
		return metrics, nil
	}
	if err != nil {
		return nanoflare.DatabaseMetrics{}, err
	}
	metrics.Available = true
	metrics.P50DurationMS = postgresDatabaseDurationPercentile(metrics, 0.50)
	metrics.P99DurationMS = postgresDatabaseDurationPercentile(metrics, 0.99)
	return metrics, nil
}

func (p *Postgres) RecordDatabaseQueryMetrics(input nanoflare.DatabaseQueryMetricsInput) error {
	if _, err := p.GetDatabase(input.DatabaseID); err != nil {
		return err
	}
	readQueries := int64(1)
	writeQueries := int64(0)
	if input.ChangedDB || input.RowsWritten > 0 {
		readQueries = 0
		writeQueries = 1
	}
	bucketColumn := databaseDurationBucketColumn(input.DurationMS)
	_, err := p.db.Exec(fmt.Sprintf(`
INSERT INTO database_metrics (
	database_id, queries, read_queries, write_queries, rows_read, rows_returned, rows_written,
	storage_bytes, table_count, total_duration_ms, %s
) VALUES ($1, 1, $2, $3, $4, $5, $6, GREATEST($7, 0), GREATEST($8, 0), $9, 1)
ON CONFLICT (database_id) DO UPDATE SET
	queries = database_metrics.queries + 1,
	read_queries = database_metrics.read_queries + $2,
	write_queries = database_metrics.write_queries + $3,
	rows_read = database_metrics.rows_read + $4,
	rows_returned = database_metrics.rows_returned + $5,
	rows_written = database_metrics.rows_written + $6,
	storage_bytes = GREATEST($7, 0),
	table_count = GREATEST($8, 0),
	total_duration_ms = database_metrics.total_duration_ms + $9,
	%s = database_metrics.%s + 1`, bucketColumn, bucketColumn, bucketColumn),
		input.DatabaseID, readQueries, writeQueries, input.RowsRead, input.RowsReturned, input.RowsWritten, input.SizeAfter, input.TableCount, input.DurationMS)
	return err
}

func (p *Postgres) UpdateDatabaseRuntimeStats(databaseID string, stats nanoflare.DatabaseRuntimeStats) error {
	if _, err := p.GetDatabase(databaseID); err != nil {
		return err
	}
	_, err := p.db.Exec(`
INSERT INTO database_metrics (database_id, storage_bytes, table_count) VALUES ($1, GREATEST($2, 0), GREATEST($3, 0))
ON CONFLICT (database_id) DO UPDATE SET storage_bytes = GREATEST($2, 0), table_count = GREATEST($3, 0)`,
		databaseID, stats.StorageBytes, stats.TableCount)
	return err
}

func (p *Postgres) CreateObjectStorageBucket(bucket nanoflare.ObjectStorageBucket) error {
	_, err := p.db.Exec(`INSERT INTO object_storage_buckets (id, org_id, name, external_id, oauth_client_id, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		bucket.ID, bucket.OrgID, bucket.Name, bucket.ExternalID, bucket.OAuthClientID, bucket.CreatedAt)
	if isUniqueViolation(err) {
		return nanoflare.ErrObjectStorageBucketExists
	}
	return err
}

func (p *Postgres) CountObjectStorageBucketsByOrg(orgID string) (int, error) {
	var count int
	err := p.db.QueryRow(`SELECT count(*) FROM object_storage_buckets WHERE org_id = $1`, orgID).Scan(&count)
	return count, err
}

func (p *Postgres) ListObjectStorageBuckets() ([]nanoflare.ObjectStorageBucket, error) {
	return p.ListObjectStorageBucketsByOrg("")
}

func (p *Postgres) ListObjectStorageBucketsByOrg(orgID string) ([]nanoflare.ObjectStorageBucket, error) {
	query := `SELECT id, org_id, name, external_id, oauth_client_id, created_at FROM object_storage_buckets`
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
		if err := rows.Scan(&bucket.ID, &bucket.OrgID, &bucket.Name, &bucket.ExternalID, &bucket.OAuthClientID, &bucket.CreatedAt); err != nil {
			return nil, err
		}
		buckets = append(buckets, bucket)
	}
	return buckets, rows.Err()
}

func (p *Postgres) GetObjectStorageBucket(bucketID string) (nanoflare.ObjectStorageBucket, error) {
	var bucket nanoflare.ObjectStorageBucket
	err := p.db.QueryRow(`SELECT id, org_id, name, external_id, oauth_client_id, created_at FROM object_storage_buckets WHERE id = $1`, bucketID).
		Scan(&bucket.ID, &bucket.OrgID, &bucket.Name, &bucket.ExternalID, &bucket.OAuthClientID, &bucket.CreatedAt)
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
	query := `SELECT id, org_id, name, hostname, auth, external_id, oauth_client_id, created_by, runtime_token, created_at FROM workers`
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
	workers := make([]nanoflare.App, 0)
	for rows.Next() {
		var app nanoflare.App
		var auth []byte
		if err := rows.Scan(&app.ID, &app.OrgID, &app.Name, &app.Hostname, &auth, &app.ExternalID, &app.OAuthClientID, &app.CreatedBy, &app.RuntimeToken, &app.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(auth, &app.Auth); err != nil {
			return nil, err
		}
		workers = append(workers, app)
	}
	return workers, rows.Err()
}

func (p *Postgres) getApp(appID string) (nanoflare.App, error) {
	var app nanoflare.App
	var auth []byte
	err := p.db.QueryRow(`SELECT id, org_id, name, hostname, auth, external_id, oauth_client_id, created_by, runtime_token, created_at FROM workers WHERE id = $1`, appID).
		Scan(&app.ID, &app.OrgID, &app.Name, &app.Hostname, &auth, &app.ExternalID, &app.OAuthClientID, &app.CreatedBy, &app.RuntimeToken, &app.CreatedAt)
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
	result, err := p.db.Exec(`UPDATE workers SET name = $2, hostname = $3, auth = $4 WHERE id = $1`,
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
	if _, err := tx.Exec(`DELETE FROM deployments WHERE worker_id = $1`, appID); err != nil {
		return err
	}
	result, err := tx.Exec(`DELETE FROM workers WHERE id = $1`, appID)
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
	rows, err := p.db.Query(`SELECT name, nonce, ciphertext, created_at, updated_at FROM worker_secrets WHERE worker_id = $1 ORDER BY name`, appID)
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
INSERT INTO worker_secrets (worker_id, name, nonce, ciphertext, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (worker_id, name)
DO UPDATE SET nonce = EXCLUDED.nonce, ciphertext = EXCLUDED.ciphertext, updated_at = EXCLUDED.updated_at`,
		appID, secret.Name, secret.Nonce, secret.Ciphertext, secret.CreatedAt, secret.UpdatedAt)
	if isForeignKeyViolation(err) {
		return nanoflare.ErrAppNotFound
	}
	return err
}

func (p *Postgres) DeleteSecret(appID, name string) error {
	result, err := p.db.Exec(`DELETE FROM worker_secrets WHERE worker_id = $1 AND name = $2`, appID, name)
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
	result, err := tx.Exec(`UPDATE deployments SET active = false, traffic_percent = 0 WHERE worker_id = $1 AND active`, deployment.AppID)
	if err != nil {
		return err
	}
	_ = result
	_, err = tx.Exec(`
INSERT INTO deployments
	(id, worker_id, commit_hash, commit_message, created_by, files, assets, entrypoint, format, compatibility_date, compatibility_flags, triggers, vars, kv_namespaces, db, object_storage_bucket, asset_config, bundle_size, object_key, port, created_at, active, traffic_percent)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, true, 100)`,
		deployment.ID, deployment.AppID, deployment.CommitHash, deployment.CommitMessage, deployment.CreatedBy, files, assets, deployment.Entrypoint, deployment.Format,
		deployment.CompatibilityDate, mustJSON(deployment.CompatibilityFlags), mustJSON(deployment.Triggers), mustJSON(deployment.Vars), mustJSON(deployment.KVNamespaces), mustJSON(deployment.Databases), mustJSON(deployment.ObjectStorageBuckets), mustJSON(deployment.AssetConfig), deployment.BundleSize, deployment.ObjectKey, deployment.Port, deployment.CreatedAt)
	if isForeignKeyViolation(err) {
		return nanoflare.ErrAppNotFound
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Postgres) SetActive(appID, deploymentID string) error {
	if deploymentID == "" {
		return p.SetActiveTraffic(appID, nil)
	}
	return p.SetActiveTraffic(appID, []nanoflare.DeploymentTraffic{{ID: deploymentID, TrafficPercent: 100}})
}

func (p *Postgres) SetActiveTraffic(appID string, traffic []nanoflare.DeploymentTraffic) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE deployments SET active = false, traffic_percent = 0 WHERE worker_id = $1 AND active`, appID); err != nil {
		return err
	}
	if len(traffic) == 0 {
		return tx.Commit()
	}
	for _, item := range traffic {
		result, err := tx.Exec(`UPDATE deployments SET active = true, traffic_percent = $3 WHERE worker_id = $1 AND id = $2`, appID, item.ID, item.TrafficPercent)
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
	}
	return tx.Commit()
}

func (p *Postgres) DeleteDeployment(id string) error {
	_, err := p.db.Exec(`DELETE FROM deployments WHERE id = $1`, id)
	return err
}

func (p *Postgres) ActiveDeployments() ([]nanoflare.ActiveDeployment, error) {
	rows, err := p.db.Query(`
SELECT a.id, a.org_id, a.name, a.hostname, a.auth, a.external_id, a.oauth_client_id, a.created_by, a.runtime_token, a.created_at,
	d.id, d.worker_id, d.commit_hash, d.commit_message, d.created_by, d.files, d.assets, d.entrypoint, d.format, d.compatibility_date, d.compatibility_flags, d.triggers, d.vars, d.kv_namespaces, d.db, d.object_storage_bucket, d.asset_config, d.bundle_size, d.object_key, d.port, d.created_at, d.traffic_percent
FROM deployments d
JOIN workers a ON a.id = d.worker_id
WHERE d.traffic_percent > 0
ORDER BY a.id, d.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var active []nanoflare.ActiveDeployment
	for rows.Next() {
		var item nanoflare.ActiveDeployment
		var files, assets, compatibilityFlags, triggers, vars, kvNamespaces, databases, objectStorageBuckets, assetConfig, auth []byte
		err := rows.Scan(
			&item.App.ID, &item.App.OrgID, &item.App.Name, &item.App.Hostname, &auth, &item.App.ExternalID, &item.App.OAuthClientID, &item.App.CreatedBy, &item.App.RuntimeToken, &item.App.CreatedAt,
			&item.Deployment.ID, &item.Deployment.AppID, &item.Deployment.CommitHash, &item.Deployment.CommitMessage, &item.Deployment.CreatedBy, &files, &assets, &item.Deployment.Entrypoint,
			&item.Deployment.Format, &item.Deployment.CompatibilityDate, &compatibilityFlags, &triggers, &vars, &kvNamespaces, &databases, &objectStorageBuckets, &assetConfig, &item.Deployment.BundleSize,
			&item.Deployment.ObjectKey, &item.Deployment.Port,
			&item.Deployment.CreatedAt, &item.TrafficPercent,
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
		if err := json.Unmarshal(compatibilityFlags, &item.Deployment.CompatibilityFlags); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(triggers, &item.Deployment.Triggers); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(vars, &item.Deployment.Vars); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(kvNamespaces, &item.Deployment.KVNamespaces); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(databases, &item.Deployment.Databases); err != nil {
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
	SELECT a.id, a.org_id, a.name, a.hostname, a.auth, a.external_id, a.oauth_client_id, a.created_by, a.runtime_token, a.created_at,
		d.id, d.worker_id, d.commit_hash, d.commit_message, d.created_by, d.assets, d.entrypoint, d.format, d.compatibility_date, d.compatibility_flags, d.triggers, d.vars, d.kv_namespaces, d.db, d.object_storage_bucket, d.asset_config, d.bundle_size, d.object_key, d.port, d.created_at, d.active, d.traffic_percent
	FROM deployments d
	JOIN workers a ON a.id = d.worker_id
	ORDER BY d.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []nanoflare.DeploymentRecord
	for rows.Next() {
		var item nanoflare.DeploymentRecord
		var assets, compatibilityFlags, triggers, vars, kvNamespaces, databases, objectStorageBuckets, assetConfig, auth []byte
		err := rows.Scan(
			&item.App.ID, &item.App.OrgID, &item.App.Name, &item.App.Hostname, &auth, &item.App.ExternalID, &item.App.OAuthClientID, &item.App.CreatedBy, &item.App.RuntimeToken, &item.App.CreatedAt,
			&item.Deployment.ID, &item.Deployment.AppID, &item.Deployment.CommitHash, &item.Deployment.CommitMessage, &item.Deployment.CreatedBy, &assets, &item.Deployment.Entrypoint,
			&item.Deployment.Format, &item.Deployment.CompatibilityDate, &compatibilityFlags, &triggers, &vars, &kvNamespaces, &databases, &objectStorageBuckets, &assetConfig, &item.Deployment.BundleSize,
			&item.Deployment.ObjectKey, &item.Deployment.Port,
			&item.Deployment.CreatedAt, &item.Active, &item.TrafficPercent,
		)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(assets, &item.Deployment.Assets); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(compatibilityFlags, &item.Deployment.CompatibilityFlags); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(triggers, &item.Deployment.Triggers); err != nil {
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
		if err := json.Unmarshal(databases, &item.Deployment.Databases); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(objectStorageBuckets, &item.Deployment.ObjectStorageBuckets); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(assetConfig, &item.Deployment.AssetConfig); err != nil {
			return nil, err
		}
		item.Active = item.TrafficPercent > 0
		records = append(records, item)
	}
	return records, rows.Err()
}

func (p *Postgres) AppIDForCapability(capability string) (string, error) {
	var appID string
	err := p.db.QueryRow(`SELECT id FROM workers WHERE runtime_token = $1`, capability).Scan(&appID)
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
SELECT reads, writes, size
FROM kv_namespace_metrics
WHERE kv_namespace_id = $1`, namespaceID).Scan(&metrics.Reads, &metrics.Writes, &metrics.Size)
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
INSERT INTO kv_namespace_metrics (kv_namespace_id, reads, writes, size) VALUES ($1, 1, 0, 0)
ON CONFLICT (kv_namespace_id) DO UPDATE SET reads = kv_namespace_metrics.reads + 1`, namespaceID)
	return err
}

func (p *Postgres) IncrementKVNamespaceWrites(namespaceID string) error {
	if _, err := p.GetKVNamespace(namespaceID); err != nil {
		return err
	}
	_, err := p.db.Exec(`
INSERT INTO kv_namespace_metrics (kv_namespace_id, reads, writes, size) VALUES ($1, 0, 1, 0)
ON CONFLICT (kv_namespace_id) DO UPDATE SET writes = kv_namespace_metrics.writes + 1`, namespaceID)
	return err
}

func (p *Postgres) AdjustKVNamespaceSize(namespaceID string, delta int64) error {
	if _, err := p.GetKVNamespace(namespaceID); err != nil {
		return err
	}
	_, err := p.db.Exec(`
INSERT INTO kv_namespace_metrics (kv_namespace_id, reads, writes, size) VALUES ($1, 0, 0, GREATEST($2, 0))
ON CONFLICT (kv_namespace_id) DO UPDATE SET size = GREATEST(kv_namespace_metrics.size + $2, 0)`, namespaceID, delta)
	return err
}

func (p *Postgres) KVStorageBytesByOrg(orgID string) (int64, error) {
	var size int64
	err := p.db.QueryRow(`
SELECT COALESCE(SUM(m.size), 0)
FROM kv_namespaces n
LEFT JOIN kv_namespace_metrics m ON m.kv_namespace_id = n.id
WHERE n.org_id = $1`, orgID).Scan(&size)
	return size, err
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

func (p *Postgres) ObjectStorageBytesByOrg(orgID string) (int64, error) {
	var size int64
	err := p.db.QueryRow(`
SELECT COALESCE(SUM(m.size), 0)
FROM object_storage_buckets b
LEFT JOIN object_storage_bucket_metrics m ON m.bucket_id = b.id
WHERE b.org_id = $1`, orgID).Scan(&size)
	return size, err
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

func databaseDurationBucketColumn(durationMS float64) string {
	switch {
	case durationMS <= 0.5:
		return "duration_bucket_0_5"
	case durationMS <= 1:
		return "duration_bucket_1"
	case durationMS <= 2.5:
		return "duration_bucket_2_5"
	case durationMS <= 5:
		return "duration_bucket_5"
	case durationMS <= 10:
		return "duration_bucket_10"
	case durationMS <= 25:
		return "duration_bucket_25"
	case durationMS <= 50:
		return "duration_bucket_50"
	case durationMS <= 100:
		return "duration_bucket_100"
	case durationMS <= 250:
		return "duration_bucket_250"
	case durationMS <= 500:
		return "duration_bucket_500"
	case durationMS <= 1000:
		return "duration_bucket_1000"
	default:
		return "duration_bucket_inf"
	}
}

func postgresDatabaseDurationPercentile(metrics nanoflare.DatabaseMetrics, percentile float64) float64 {
	if metrics.Queries <= 0 {
		return 0
	}
	target := int64(float64(metrics.Queries) * percentile)
	if target < 1 {
		target = 1
	}
	var seen int64
	for _, bucket := range []struct {
		upper float64
		count int64
	}{
		{0.5, metrics.DurationBucket0_5},
		{1, metrics.DurationBucket1},
		{2.5, metrics.DurationBucket2_5},
		{5, metrics.DurationBucket5},
		{10, metrics.DurationBucket10},
		{25, metrics.DurationBucket25},
		{50, metrics.DurationBucket50},
		{100, metrics.DurationBucket100},
		{250, metrics.DurationBucket250},
		{500, metrics.DurationBucket500},
		{1000, metrics.DurationBucket1000},
		{1000, metrics.DurationBucketInf},
	} {
		seen += bucket.count
		if seen >= target {
			return bucket.upper
		}
	}
	return 1000
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
