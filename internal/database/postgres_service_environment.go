package database

import (
	"database/sql"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func (p *Postgres) ListServiceEnvironments(org, service string) ([]nanoflare.ServiceEnvironment, error) {
	rows, err := p.db.Query(`SELECT org_id,service_id,service_generation,name,worker_id FROM service_environments WHERE org_id=$1 AND service_id=$2 ORDER BY name`, org, service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []nanoflare.ServiceEnvironment{}
	for rows.Next() {
		var item nanoflare.ServiceEnvironment
		if err := rows.Scan(&item.OrgID, &item.ServiceID, &item.ServiceGeneration, &item.Name, &item.WorkerID); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (p *Postgres) CreateServiceEnvironment(item nanoflare.ServiceEnvironment, app nanoflare.App) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var generation bool
	if err := tx.QueryRow(`SELECT created_at=$3 FROM workers WHERE id=$1 AND org_id=$2 FOR UPDATE`, item.ServiceID, item.OrgID, item.ServiceGeneration).Scan(&generation); err != nil || !generation {
		return nanoflare.ErrAppNotFound
	}
	_, err = tx.Exec(`INSERT INTO workers (id,org_id,name,hostname,auth,external_id,oauth_client_id,created_by,runtime_token,created_at,platform) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, app.ID, app.OrgID, app.Name, app.Hostname, mustJSON(app.Auth), app.ExternalID, app.OAuthClientID, app.CreatedBy, app.RuntimeToken, app.CreatedAt, mustJSON(app.Platform))
	if err != nil {
		return err
	}
	result, err := tx.Exec(`INSERT INTO service_environments (org_id,service_id,service_generation,name,worker_id) VALUES ($1,$2,$3,$4,$5) ON CONFLICT(service_id,name) DO UPDATE SET service_generation=excluded.service_generation,worker_id=excluded.worker_id WHERE service_environments.service_generation<>excluded.service_generation OR NOT EXISTS(SELECT 1 FROM workers WHERE id=service_environments.worker_id)`, item.OrgID, item.ServiceID, item.ServiceGeneration, item.Name, item.WorkerID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return nanoflare.ErrAppExists
	}
	return tx.Commit()
}

func (p *Postgres) OrdinaryServiceWorkerID(org, name string) (string, error) {
	var id string
	err := p.db.QueryRow(`SELECT worker_id FROM ordinary_services WHERE org_id=$1 AND name=$2`, org, name).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nanoflare.ErrAppNotFound
	}
	return id, err
}
func (p *Postgres) CreateOrdinaryService(org, name string, app nanoflare.App) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO workers (id,org_id,name,hostname,auth,external_id,oauth_client_id,created_by,runtime_token,created_at,platform) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, app.ID, app.OrgID, app.Name, app.Hostname, mustJSON(app.Auth), app.ExternalID, app.OAuthClientID, app.CreatedBy, app.RuntimeToken, app.CreatedAt, mustJSON(app.Platform))
	if isUniqueViolation(err) {
		return nanoflare.ErrAppExists
	}
	if err != nil {
		return err
	}
	result, err := tx.Exec(`INSERT INTO ordinary_services(org_id,name,worker_id) VALUES($1,$2,$3) ON CONFLICT(org_id,name) DO UPDATE SET worker_id=excluded.worker_id WHERE NOT EXISTS(SELECT 1 FROM workers WHERE id=ordinary_services.worker_id)`, org, name, app.ID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return nanoflare.ErrAppExists
	}
	return tx.Commit()
}
