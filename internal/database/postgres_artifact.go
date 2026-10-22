package database

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func (p *Postgres) migrateArtifacts(ctx context.Context) error {
	_, err := p.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS deployment_artifacts (
 deployment_id text PRIMARY KEY,worker_id text NOT NULL REFERENCES workers(id) ON DELETE CASCADE,digest text NOT NULL,payload jsonb NOT NULL);
 CREATE TABLE IF NOT EXISTS deployment_publications (
 worker_id text NOT NULL REFERENCES workers(id) ON DELETE CASCADE,request_key text NOT NULL,fingerprint text NOT NULL,deployment_id text NOT NULL,payload jsonb NOT NULL,PRIMARY KEY(worker_id,request_key));`)
	return err
}
func (p *Postgres) PutDeploymentArtifact(a nanoflare.DeploymentArtifact) error {
	result, err := p.db.Exec(`INSERT INTO deployment_artifacts VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, a.Deployment.ID, a.Deployment.AppID, a.Digest, mustJSON(a))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	prior, err := p.GetDeploymentArtifact(a.Deployment.AppID, a.Deployment.ID)
	left, _ := json.Marshal(prior)
	right, _ := json.Marshal(a)
	var l, r any
	ld, rd := json.NewDecoder(bytes.NewReader(left)), json.NewDecoder(bytes.NewReader(right))
	ld.UseNumber()
	rd.UseNumber()
	le, re := ld.Decode(&l), rd.Decode(&r)
	left, _ = json.Marshal(l)
	right, _ = json.Marshal(r)
	if err != nil || le != nil || re != nil || !bytes.Equal(left, right) {
		return nanoflare.ErrArtifactConflict
	}
	return nil
}
func (p *Postgres) GetDeploymentArtifact(app, id string) (nanoflare.DeploymentArtifact, error) {
	var payload []byte
	var a nanoflare.DeploymentArtifact
	err := p.db.QueryRow(`SELECT payload FROM deployment_artifacts WHERE worker_id=$1 AND deployment_id=$2`, app, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return a, nanoflare.ErrArtifactNotFound
	}
	if err != nil {
		return a, err
	}
	err = json.Unmarshal(payload, &a)
	a.Deployment.ObjectKey = a.ObjectKey
	a.Deployment.BundleSize = a.BundleSize
	return a, err
}
func (p *Postgres) StoreDeployment(d nanoflare.Deployment) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = storeDeploymentTx(tx, d); err != nil {
		return err
	}
	return tx.Commit()
}
func (p *Postgres) GetPublication(app, key string) (nanoflare.Publication, bool, error) {
	var data []byte
	var op nanoflare.Publication
	err := p.db.QueryRow(`SELECT payload FROM deployment_publications WHERE worker_id=$1 AND request_key=$2`, app, key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return op, false, nil
	}
	if err != nil {
		return op, false, err
	}
	err = json.Unmarshal(data, &op)
	return op, true, err
}
func (p *Postgres) SavePublication(op nanoflare.Publication) error {
	result, err := p.db.Exec(`INSERT INTO deployment_publications VALUES ($1,$2,$3,$4,$5) ON CONFLICT(worker_id,request_key) DO UPDATE SET payload=EXCLUDED.payload WHERE deployment_publications.fingerprint=EXCLUDED.fingerprint AND deployment_publications.deployment_id=EXCLUDED.deployment_id`, op.AppID, op.Key, op.Fingerprint, op.DeploymentID, mustJSON(op))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return nanoflare.ErrIdempotencyConflict
	}
	return nil
}

func (p *Postgres) ListPublications() ([]nanoflare.Publication, error) {
	rows, err := p.db.Query(`SELECT payload FROM deployment_publications ORDER BY payload->>'created_at', worker_id, request_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []nanoflare.Publication{}
	for rows.Next() {
		var payload []byte
		var op nanoflare.Publication
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &op); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}
