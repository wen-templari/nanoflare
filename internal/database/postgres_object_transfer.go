package database

import (
	"encoding/json"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func (p *Postgres) ListObjectTransfers(bucketID, orgID, includeID string) ([]nanoflare.ObjectTransfer, error) {
	query := `SELECT transfer FROM object_transfers`
	var args []any
	switch {
	case orgID != "":
		query += ` WHERE transfer->>'OrgID' = $1`
		args = append(args, orgID)
	case bucketID != "":
		query += ` WHERE transfer->>'BucketID' = $1`
		args = append(args, bucketID)
	default:
		query += ` WHERE (NOT COALESCE((transfer->>'Cleaned')::boolean, false)
			AND (transfer->>'Result' IS NOT NULL OR (transfer->>'Aborted')::boolean)) OR (transfer->>'ExpiresAt')::timestamptz < $1`
		args = append(args, time.Now().UTC())
	}
	if bucketID != "" || orgID != "" {
		query += ` AND (id = $2 OR NOT COALESCE((transfer->>'Cleaned')::boolean, false))`
		args = append(args, includeID)
	}
	rows, err := p.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var transfers []nanoflare.ObjectTransfer
	for rows.Next() {
		var data []byte
		var transfer nanoflare.ObjectTransfer
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &transfer); err != nil {
			return nil, err
		}
		transfers = append(transfers, transfer)
	}
	return transfers, rows.Err()
}

func (p *Postgres) SaveObjectTransfer(transfer nanoflare.ObjectTransfer) error {
	data, err := json.Marshal(transfer)
	if err != nil {
		return err
	}
	_, err = p.db.Exec(`INSERT INTO object_transfers (id, transfer) VALUES ($1, $2)
ON CONFLICT (id) DO UPDATE SET transfer = EXCLUDED.transfer`, transfer.ID, string(data))
	return err
}

func (p *Postgres) DeleteObjectTransfer(id string) error {
	_, err := p.db.Exec(`DELETE FROM object_transfers WHERE id = $1`, id)
	return err
}

func (p *Postgres) MarkObjectTransferCleaned(id string) error {
	_, err := p.db.Exec(`UPDATE object_transfers SET transfer = jsonb_set(transfer, '{Cleaned}', 'true') WHERE id = $1`, id)
	return err
}
