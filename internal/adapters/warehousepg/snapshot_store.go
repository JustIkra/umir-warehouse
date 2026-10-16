package warehousepg

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/postgres"
	"github.com/JustIkra/umir-warehouse/internal/application"
	"github.com/JustIkra/umir-warehouse/internal/domain"
	"github.com/jackc/pgx/v5"
)

type SnapshotStore struct {
	db    *postgres.DB
	scope postgres.Scope
}

var _ application.SnapshotStore = (*SnapshotStore)(nil)

func (s *SnapshotStore) LoadLatest(ctx context.Context) (domain.Snapshot, bool, error) {
	if s == nil || s.db == nil {
		return domain.Snapshot{}, false, contract.ErrRuntimeNotReady
	}
	var snapshot domain.Snapshot
	found := false
	err := s.db.InScopedTx(ctx, s.scope, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT last_sequence, schema_version, state::text, state_sha256
			FROM snapshots WHERE restaurant_id = $1 ORDER BY last_sequence DESC LIMIT 3`, s.scope.RestaurantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sequence int64
			var schema int16
			var stateJSON []byte
			var digest []byte
			if err := rows.Scan(&sequence, &schema, &stateJSON, &digest); err != nil {
				return err
			}
			state, err := decodeSnapshotState(stateJSON, hex.EncodeToString(digest))
			if err != nil {
				continue
			}
			candidate := domain.Snapshot{SchemaVersion: int64(schema), StreamID: domain.InventoryStreamID, LastSequence: sequence, State: state}
			if err := domain.ValidateSnapshot(candidate); err != nil {
				continue
			}
			snapshot, found = domain.CloneSnapshot(candidate), true
			break
		}
		return rows.Err()
	})
	if err != nil {
		return domain.Snapshot{}, false, mapStoreError(err)
	}
	return snapshot, found, nil
}

func (s *SnapshotStore) Save(ctx context.Context, candidate domain.Snapshot) error {
	if s == nil || s.db == nil {
		return contract.ErrRuntimeNotReady
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	candidate = domain.CloneSnapshot(candidate)
	if err := domain.ValidateSnapshot(candidate); err != nil {
		return err
	}
	state, hash, err := encodeSnapshotState(candidate.State)
	if err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}
	digest, err := hex.DecodeString(hash)
	if err != nil {
		return err
	}
	err = s.db.InScopedTx(ctx, s.scope, pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO snapshots (restaurant_id, last_sequence, schema_version, state, state_sha256, created_at)
			SELECT $1, $2, $3, $4, $5, clock_timestamp()
			 WHERE NOT EXISTS (SELECT 1 FROM snapshots WHERE restaurant_id = $1 AND last_sequence >= $2)
			ON CONFLICT (restaurant_id, last_sequence) DO NOTHING`,
			s.scope.RestaurantID, candidate.LastSequence, candidate.SchemaVersion, state, digest)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM snapshots WHERE restaurant_id = $1 AND last_sequence NOT IN (
			SELECT last_sequence FROM snapshots WHERE restaurant_id = $1 ORDER BY last_sequence DESC LIMIT 3)`, s.scope.RestaurantID)
		return err
	})
	return mapStoreError(err)
}
