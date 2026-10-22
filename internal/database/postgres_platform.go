package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/lib/pq"
	"strings"
)

func (p *Postgres) migratePlatform(ctx context.Context) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS dispatch_namespaces (
 id text PRIMARY KEY, org_id text NOT NULL, name text NOT NULL, trust text NOT NULL CHECK(trust='trusted'),
 customer_mode text NOT NULL CHECK(customer_mode IN ('organization','platform')), policy_revision bigint NOT NULL DEFAULT 1,
 grants jsonb NOT NULL DEFAULT '[]', created_at timestamptz NOT NULL, UNIQUE(org_id,name));
 CREATE TABLE IF NOT EXISTS dispatch_namespace_policies (
 namespace_id text NOT NULL REFERENCES dispatch_namespaces(id) ON DELETE CASCADE,revision bigint NOT NULL,grants jsonb NOT NULL,created_at timestamptz NOT NULL,PRIMARY KEY(namespace_id,revision));
 ALTER TABLE workers ADD COLUMN IF NOT EXISTS platform jsonb NOT NULL DEFAULT 'null';
 ALTER TABLE workers ADD COLUMN IF NOT EXISTS platform_namespace_id text GENERATED ALWAYS AS (platform->>'namespace_id') STORED REFERENCES dispatch_namespaces(id) ON DELETE RESTRICT;
 CREATE UNIQUE INDEX IF NOT EXISTS workers_namespace_script_idx ON workers(platform_namespace_id,(platform->>'script_name')) WHERE platform_namespace_id IS NOT NULL;
 DO $$ DECLARE constraint_name text; BEGIN
 FOR constraint_name IN SELECT conname FROM pg_constraint WHERE conrelid='workers'::regclass AND contype='u' AND conkey=ARRAY[(SELECT attnum FROM pg_attribute WHERE attrelid='workers'::regclass AND attname='hostname')]::smallint[]
 LOOP EXECUTE format('ALTER TABLE workers DROP CONSTRAINT %I',constraint_name); END LOOP; END $$;
 CREATE UNIQUE INDEX IF NOT EXISTS workers_hostname_present_idx ON workers(hostname) WHERE hostname<>'';
 CREATE UNIQUE INDEX IF NOT EXISTS dispatch_namespace_org_idx ON dispatch_namespaces(id,org_id);
 DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='workers'::regclass AND conname='workers_platform_org_fk') THEN
 ALTER TABLE workers ADD CONSTRAINT workers_platform_org_fk FOREIGN KEY(platform_namespace_id,org_id) REFERENCES dispatch_namespaces(id,org_id) ON DELETE RESTRICT;
 END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='workers'::regclass AND conname='workers_platform_private_host') THEN
 ALTER TABLE workers ADD CONSTRAINT workers_platform_private_host CHECK(platform='null'::jsonb OR hostname='');
 END IF;
 END $$;
 CREATE OR REPLACE VIEW dispatch_legacy_name_conflicts AS SELECT org_id,name,array_agg(id ORDER BY id) AS worker_ids FROM workers WHERE platform='null'::jsonb GROUP BY org_id,name HAVING count(*)>1;`)
	if err != nil {
		return err
	}
	// Upgrade only exact role-default scope sets. Preserve custom restrictions,
	// PAT scopes and OAuth grants rather than silently widening credentials.
	for _, role := range []string{nanoflare.RoleOwner, nanoflare.RoleAdmin, nanoflare.RoleMember, nanoflare.RoleViewer} {
		current := nanoflare.RoleScopes(role)
		old := []string{}
		for _, scope := range current {
			if !strings.HasPrefix(scope, "namespaces:") {
				old = append(old, scope)
			}
		}
		for _, table := range []string{"user_organizations", "organization_invites"} {
			if _, err = tx.ExecContext(ctx, `UPDATE `+table+` SET scopes=$1 WHERE role=$2 AND scopes @> $3::jsonb AND scopes <@ $3::jsonb`, mustJSON(current), role, mustJSON(old)); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
func platformError(err error) error {
	if isUniqueViolation(err) {
		return nanoflare.ErrDispatchNamespaceExists
	}
	var p *pq.Error
	if errors.As(err, &p) && p.Code == "23503" {
		return nanoflare.ErrDispatchNamespaceInUse
	}
	return err
}
func scanDispatchNamespace(row interface{ Scan(...any) error }) (nanoflare.DispatchNamespace, error) {
	var n nanoflare.DispatchNamespace
	var grants []byte
	err := row.Scan(&n.ID, &n.OrgID, &n.Name, &n.Trust, &n.CustomerMode, &n.PolicyRevision, &grants, &n.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return n, nanoflare.ErrDispatchNamespaceNotFound
	}
	if err == nil {
		err = json.Unmarshal(grants, &n.Grants)
	}
	return n, err
}

const namespaceColumns = `id,org_id,name,trust,customer_mode,policy_revision,grants,created_at`

func (p *Postgres) CreateDispatchNamespace(ns nanoflare.DispatchNamespace) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO dispatch_namespaces (`+namespaceColumns+`) VALUES ($1,$2,$3,$4,$5,1,$6,$7)`, ns.ID, ns.OrgID, ns.Name, ns.Trust, ns.CustomerMode, mustJSON(ns.Grants), ns.CreatedAt)
	if err != nil {
		return platformError(err)
	}
	_, err = tx.Exec(`INSERT INTO dispatch_namespace_policies VALUES ($1,1,$2,$3)`, ns.ID, mustJSON(ns.Grants), ns.CreatedAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (p *Postgres) GetDispatchNamespace(id string) (nanoflare.DispatchNamespace, error) {
	return scanDispatchNamespace(p.db.QueryRow(`SELECT `+namespaceColumns+` FROM dispatch_namespaces WHERE id=$1`, id))
}
func (p *Postgres) ListDispatchNamespaces(org string) ([]nanoflare.DispatchNamespace, error) {
	rows, err := p.db.Query(`SELECT `+namespaceColumns+` FROM dispatch_namespaces WHERE org_id=$1 ORDER BY name`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []nanoflare.DispatchNamespace{}
	for rows.Next() {
		ns, e := scanDispatchNamespace(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, ns)
	}
	return out, rows.Err()
}
func (p *Postgres) RenameDispatchNamespace(id, name string) error {
	result, err := p.db.Exec(`UPDATE dispatch_namespaces SET name=$2 WHERE id=$1`, id, name)
	if err != nil {
		return platformError(err)
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return nanoflare.ErrDispatchNamespaceNotFound
	}
	return nil
}
func (p *Postgres) DeleteDispatchNamespace(id string) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ns, err := scanDispatchNamespace(tx.QueryRow(`SELECT `+namespaceColumns+` FROM dispatch_namespaces WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return err
	}
	if len(ns.Grants) > 0 {
		return nanoflare.ErrDispatchNamespaceInUse
	}
	_, err = tx.Exec(`DELETE FROM dispatch_namespaces WHERE id=$1`, id)
	if err != nil {
		return platformError(err)
	}
	return platformError(tx.Commit())
}
func (p *Postgres) SetNamespaceGrants(id string, expected int64, grants []nanoflare.NamespaceResourceGrant) (nanoflare.DispatchNamespace, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return nanoflare.DispatchNamespace{}, err
	}
	defer tx.Rollback()
	ns, err := scanDispatchNamespace(tx.QueryRow(`UPDATE dispatch_namespaces SET grants=$3,policy_revision=policy_revision+1 WHERE id=$1 AND policy_revision=$2 RETURNING `+namespaceColumns, id, expected, mustJSON(grants)))
	if errors.Is(err, nanoflare.ErrDispatchNamespaceNotFound) {
		if _, e := scanDispatchNamespace(tx.QueryRow(`SELECT `+namespaceColumns+` FROM dispatch_namespaces WHERE id=$1`, id)); e != nil {
			return ns, e
		}
		return ns, nanoflare.ErrNamespacePolicyConflict
	}
	if err != nil {
		return ns, err
	}
	_, err = tx.Exec(`INSERT INTO dispatch_namespace_policies VALUES ($1,$2,$3,now())`, id, ns.PolicyRevision, mustJSON(grants))
	if err != nil {
		return ns, err
	}
	return ns, tx.Commit()
}
func (p *Postgres) NamespacePolicies(id string) ([]nanoflare.NamespacePolicy, error) {
	rows, err := p.db.Query(`SELECT namespace_id,revision,grants,created_at FROM dispatch_namespace_policies WHERE namespace_id=$1 ORDER BY revision`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []nanoflare.NamespacePolicy{}
	for rows.Next() {
		var policy nanoflare.NamespacePolicy
		var data []byte
		if err = rows.Scan(&policy.NamespaceID, &policy.Revision, &data, &policy.CreatedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &policy.Grants); err != nil {
			return nil, err
		}
		out = append(out, policy)
	}
	return out, rows.Err()
}
