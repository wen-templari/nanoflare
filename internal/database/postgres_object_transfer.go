package database

import (
	"encoding/json"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func (p *Postgres) ListObjectTransfers() ([]nanoflare.ObjectTransfer, error) {
	rows, err := p.db.Query(`SELECT transfer FROM object_transfers`)
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
