package database

import (
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/lib/pq"
)

func (p *Postgres) DeleteNamespaceScripts(org, namespace string, ids []string) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM dispatch_namespaces WHERE id=$1 AND org_id=$2)`, namespace, org).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nanoflare.ErrDispatchNamespaceNotFound
	}
	if len(ids) == 0 {
		return tx.Commit()
	}
	rows, err := tx.Query(`SELECT id FROM workers WHERE id=ANY($1) AND org_id=$2 AND platform_namespace_id=$3 AND platform->>'kind'='customer' ORDER BY id FOR UPDATE`, pq.Array(ids), org, namespace)
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if count != len(ids) {
		return nanoflare.ErrNamespaceDeletionConflict
	}
	if _, err = tx.Exec(`DELETE FROM deployments WHERE worker_id=ANY($1)`, pq.Array(ids)); err != nil {
		return err
	}
	result, err := tx.Exec(`DELETE FROM workers WHERE id=ANY($1)`, pq.Array(ids))
	if err != nil {
		return err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if deleted != int64(len(ids)) {
		return nanoflare.ErrNamespaceDeletionConflict
	}
	return tx.Commit()
}
