package warehousepg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/postgres"
	"github.com/JustIkra/umir-warehouse/internal/application"
	"github.com/JustIkra/umir-warehouse/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type EventStore struct {
	db               *postgres.DB
	scope            postgres.Scope
	generation       string
	lifecycleVersion int64
}

var _ application.EventStore = (*EventStore)(nil)

func (s *EventStore) AppendBatch(ctx context.Context, batch domain.CommandBatch) (domain.AppendResult, error) {
	if s == nil || s.db == nil || !validCommandBatch(batch) {
		return domain.AppendResult{}, domain.ErrInvalidEventBatch
	}
	var result domain.AppendResult
	err := s.db.InScopedTx(ctx, s.scope, pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		existing, found, err := s.findCommandTx(ctx, tx, batch.CommandID)
		if err != nil {
			return err
		}
		if found {
			if existing.Command.CommandType != batch.CommandType || existing.Command.CommandFingerprint != batch.CommandFingerprint {
				return domain.ErrCommandIdentityConflict
			}
			existing.Replayed = true
			result = existing
			return nil
		}
		recorded, err := prepareRecordedBatch(batch, s.scope.RestaurantID, s.generation)
		if err != nil {
			return err
		}
		commandResult, err := domain.ResultForBatch(recorded)
		if err != nil {
			return err
		}
		resultBytes, err := encodeResult(commandResult)
		if err != nil {
			return fmt.Errorf("encode command result: %w", err)
		}
		if err := s.advanceHead(ctx, tx, batch.ExpectedVersion, recorded.FinalSequence); err != nil {
			return err
		}
		first := recorded.Events[0].Sequence
		last := recorded.Events[len(recorded.Events)-1]
		if _, err := tx.Exec(ctx, `
			INSERT INTO commands (restaurant_id, command_id, command_type, fingerprint,
			                      first_sequence, final_sequence, result, recorded_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			s.scope.RestaurantID, batch.CommandID, batch.CommandType, batch.CommandFingerprint,
			first, recorded.FinalSequence, resultBytes, last.RecordedAt); err != nil {
			return mapStoreError(err)
		}
		outbox := postgres.NewOutbox(s.db)
		for _, event := range recorded.Events {
			payload, err := encodeEvent(event)
			if err != nil {
				return fmt.Errorf("encode journal event: %w", err)
			}
			proposalID, orderID, deliveryID := eventIdentities(event)
			if _, err := tx.Exec(ctx, `
				INSERT INTO events (restaurant_id, sequence, event_id, command_id, event_type,
				                    schema_version, recorded_at, proposal_id, order_id, delivery_id, payload)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
				s.scope.RestaurantID, event.Sequence, event.EventID, batch.CommandID,
				string(event.Type), event.SchemaVersion, event.RecordedAt, nullableIdentity(proposalID), nullableIdentity(orderID), nullableIdentity(deliveryID), payload); err != nil {
				return mapStoreError(err)
			}
			message, err := outboxEvent(event, s.scope.OrgID, s.scope.RestaurantID, s.generation)
			if err != nil {
				return err
			}
			if _, err := outbox.AddEvent(ctx, tx, message); err != nil {
				return fmt.Errorf("append event to outbox: %w", err)
			}
		}
		result = domain.AppendResult{
			Batch: recorded,
			Command: domain.CommandRecord{CommandID: batch.CommandID, CommandType: batch.CommandType,
				CommandFingerprint: batch.CommandFingerprint, Result: commandResult},
		}
		return nil
	})
	if err != nil {
		return domain.AppendResult{}, mapStoreError(err)
	}
	return cloneAppendResult(result), nil
}

func validCommandBatch(batch domain.CommandBatch) bool {
	if batch.ExpectedVersion < 0 || batch.CommandID == "" || utf8.RuneCountInString(batch.CommandID) > 256 ||
		batch.CommandType == "" || batch.CommandFingerprint == "" || len(batch.Events) == 0 ||
		int64(len(batch.Events)) > int64(^uint64(0)>>1)-batch.ExpectedVersion {
		return false
	}
	for _, r := range batch.CommandID {
		if unicode.IsControl(r) {
			return false
		}
	}
	seen := make(map[string]struct{}, len(batch.Events))
	for _, event := range batch.Events {
		if event.EventID == "" || len(event.EventID) > 256 {
			return false
		}
		for _, r := range event.EventID {
			if r < '!' || r > '~' {
				return false
			}
		}
		if _, exists := seen[event.EventID]; exists {
			return false
		}
		seen[event.EventID] = struct{}{}
		if event.Sequence != 0 && event.Sequence != batch.ExpectedVersion+int64(len(seen)) {
			return false
		}
	}
	return true
}

func prepareRecordedBatch(batch domain.CommandBatch, restaurantID, generation string) (domain.RecordedBatch, error) {
	result := domain.RecordedBatch{
		CommandID: batch.CommandID, CommandType: batch.CommandType,
		CommandFingerprint: batch.CommandFingerprint,
		FinalSequence:      batch.ExpectedVersion + int64(len(batch.Events)),
		Events:             make([]domain.Event, len(batch.Events)),
	}
	for i, input := range batch.Events {
		event := domain.CloneEvent(input)
		event.StreamID = domain.InventoryStreamID
		event.Sequence = batch.ExpectedVersion + int64(i) + 1
		event.SchemaVersion = domain.EventSchemaVersion(event)
		event.CommandID = batch.CommandID
		event.CommandFingerprint = batch.CommandFingerprint
		event.RecordedAt = event.RecordedAt.Round(0)
		if event.TenantID != "" && event.TenantID != restaurantID || event.RuntimeEpoch != "" && event.RuntimeEpoch != generation {
			return domain.RecordedBatch{}, domain.ErrInvalidRecordedBatch
		}
		if event.Type == domain.EventBatchExpiryPending || event.Type == domain.EventBatchWriteoffConfirmed || event.Type == domain.EventDeliveryReceived && event.SchemaVersion == domain.InventorySchemaV2 {
			event.TenantID, event.RuntimeEpoch = restaurantID, generation
		}
		if err := domain.ValidateRecordedEvent(event, result, event.Sequence); err != nil {
			return domain.RecordedBatch{}, err
		}
		result.Events[i] = event
	}
	return result, nil
}

func (s *EventStore) advanceHead(ctx context.Context, tx pgx.Tx, expected, next int64) error {
	var version int64
	err := tx.QueryRow(ctx, `
		UPDATE streams s SET version = $3, updated_at = clock_timestamp()
		  FROM restaurants r
		 WHERE s.restaurant_id = $1 AND s.version = $2
		   AND r.restaurant_id = s.restaurant_id AND r.organization_id = $4
		   AND r.lifecycle_version = $5 AND r.state = 'active'
		RETURNING s.version`, s.scope.RestaurantID, expected, next, s.scope.OrgID, s.lifecycleVersion).Scan(&version)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return mapStoreError(err)
	}
	var state string
	var lifecycle int64
	lookupErr := tx.QueryRow(ctx, `SELECT state, lifecycle_version FROM restaurants WHERE restaurant_id = $1`, s.scope.RestaurantID).Scan(&state, &lifecycle)
	if errors.Is(lookupErr, pgx.ErrNoRows) || state == "closed" {
		return contract.ErrScopeDeleted
	}
	if lookupErr != nil {
		return mapStoreError(lookupErr)
	}
	if lifecycle != s.lifecycleVersion {
		return contract.ErrForbidden
	}
	return domain.ErrConcurrentModification
}

func (s *EventStore) Version(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, contract.ErrRuntimeNotReady
	}
	var version int64
	err := s.db.InScopedTx(ctx, s.scope, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT version FROM streams WHERE restaurant_id = $1`, s.scope.RestaurantID).Scan(&version)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, contract.ErrRuntimeNotReady
	}
	return version, mapStoreError(err)
}

func (s *EventStore) ReadBatches(ctx context.Context, after int64) ([]domain.RecordedBatch, error) {
	if after < 0 {
		return nil, domain.ErrInvalidRecordedBatch
	}
	var batches []domain.RecordedBatch
	err := s.db.InScopedTx(ctx, s.scope, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		batches, err = s.readBatchesTx(ctx, tx, after, 0)
		return err
	})
	return batches, mapStoreError(err)
}

func (s *EventStore) ScanBatches(ctx context.Context, after int64, fn func(domain.RecordedBatch) error) error {
	if after < 0 || fn == nil {
		return domain.ErrInvalidRecordedBatch
	}
	for {
		var page []domain.RecordedBatch
		err := s.db.InScopedTx(ctx, s.scope, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			page, err = s.readBatchesTx(ctx, tx, after, 64)
			return err
		})
		if err != nil {
			return mapStoreError(err)
		}
		if len(page) == 0 {
			return nil
		}
		for _, batch := range page {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := fn(domain.CloneRecordedBatch(batch)); err != nil {
				return err
			}
			after = batch.FinalSequence
		}
	}
}

func (s *EventStore) readBatchesTx(ctx context.Context, tx pgx.Tx, after int64, limit int) ([]domain.RecordedBatch, error) {
	var head int64
	if err := tx.QueryRow(ctx, `SELECT version FROM streams WHERE restaurant_id = $1`, s.scope.RestaurantID).Scan(&head); err != nil {
		return nil, err
	}
	if after > 0 && after < head {
		var inside bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM commands WHERE restaurant_id = $1 AND first_sequence <= $2 AND final_sequence > $2)`, s.scope.RestaurantID, after).Scan(&inside); err != nil {
			return nil, err
		}
		if inside {
			return nil, domain.ErrInvalidRecordedBatch
		}
	}
	query := `SELECT command_id, command_type, fingerprint, first_sequence, final_sequence, result
	            FROM commands WHERE restaurant_id = $1 AND final_sequence > $2 ORDER BY final_sequence`
	args := []any{s.scope.RestaurantID, after}
	if limit > 0 {
		query += ` LIMIT $3`
		args = append(args, limit)
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	type commandRow struct {
		id, kind, fingerprint string
		first, final          int64
		result                []byte
	}
	var commands []commandRow
	for rows.Next() {
		var row commandRow
		if err := rows.Scan(&row.id, &row.kind, &row.fingerprint, &row.first, &row.final, &row.result); err != nil {
			rows.Close()
			return nil, err
		}
		commands = append(commands, row)
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return nil, rowsErr
	}
	batches := make([]domain.RecordedBatch, 0, len(commands))
	for _, command := range commands {
		events, err := s.readEventsRangeTx(ctx, tx, command.first, command.final)
		if err != nil {
			return nil, err
		}
		batch := domain.RecordedBatch{CommandID: command.id, CommandType: command.kind,
			CommandFingerprint: command.fingerprint, FinalSequence: command.final, Events: events}
		if int64(len(events)) != command.final-command.first+1 || len(events) == 0 || events[0].Sequence != command.first || events[len(events)-1].Sequence != command.final {
			return nil, domain.ErrInvalidRecordedBatch
		}
		for i, event := range events {
			if event.Sequence != command.first+int64(i) || event.CommandID != command.id || event.CommandFingerprint != command.fingerprint {
				return nil, domain.ErrInvalidRecordedBatch
			}
		}
		batches = append(batches, batch)
	}
	return batches, nil
}

func (s *EventStore) readEventsRangeTx(ctx context.Context, tx pgx.Tx, first, last int64) ([]domain.Event, error) {
	rows, err := tx.Query(ctx, `SELECT sequence, event_id, event_type, schema_version, command_id, payload
		FROM events WHERE restaurant_id = $1 AND sequence BETWEEN $2 AND $3 ORDER BY sequence`, s.scope.RestaurantID, first, last)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]domain.Event, 0, last-first+1)
	for rows.Next() {
		var sequence int64
		var id, eventType, commandID string
		var schema int16
		var payload []byte
		if err := rows.Scan(&sequence, &id, &eventType, &schema, &commandID, &payload); err != nil {
			return nil, err
		}
		event, err := decodeEvent(payload)
		if err != nil || event.Sequence != sequence || event.EventID != id || string(event.Type) != eventType || event.SchemaVersion != int64(schema) || event.CommandID != commandID {
			return nil, domain.ErrInvalidRecordedBatch
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (s *EventStore) ReadEvents(ctx context.Context, after int64, limit int) ([]domain.Event, error) {
	if after < 0 || limit <= 0 {
		return nil, domain.ErrInvalidRecordedBatch
	}
	var events []domain.Event
	err := s.db.InScopedTx(ctx, s.scope, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT payload FROM events WHERE restaurant_id = $1 AND sequence > $2 ORDER BY sequence LIMIT $3`, s.scope.RestaurantID, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var payload []byte
			if err := rows.Scan(&payload); err != nil {
				return err
			}
			event, err := decodeEvent(payload)
			if err != nil {
				return domain.ErrInvalidRecordedBatch
			}
			events = append(events, event)
		}
		return rows.Err()
	})
	return events, mapStoreError(err)
}

func (s *EventStore) ReadPeriod(ctx context.Context, query application.PeriodQuery) ([]domain.Event, error) {
	if query.UpToSequence <= 0 {
		return nil, domain.ErrInvalidRecordedBatch
	}
	if !query.To.After(query.From) {
		return []domain.Event{}, nil
	}
	types := make([]string, len(query.Types))
	for i, eventType := range query.Types {
		types[i] = string(eventType)
	}
	var events []domain.Event
	err := s.db.InScopedTx(ctx, s.scope, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT payload FROM events
			WHERE restaurant_id = $1 AND sequence <= $2
			  AND recorded_at >= $3::timestamptz - interval '1 millisecond'
			  AND recorded_at < $4::timestamptz + interval '1 millisecond'
			  AND (cardinality($5::text[]) = 0 OR event_type = ANY($5::text[]))
			ORDER BY sequence`, s.scope.RestaurantID, query.UpToSequence, query.From, query.To, types)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var payload []byte
			if err := rows.Scan(&payload); err != nil {
				return err
			}
			event, err := decodeEvent(payload)
			if err != nil {
				return domain.ErrInvalidRecordedBatch
			}
			if event.RecordedAt.Before(query.From) || !event.RecordedAt.Before(query.To) || len(query.Types) > 0 && !slices.Contains(query.Types, event.Type) {
				continue
			}
			events = append(events, event)
		}
		return rows.Err()
	})
	return events, mapStoreError(err)
}

func (s *EventStore) FindOrderPlacement(ctx context.Context, orderID string) (application.OrderPlacement, bool, error) {
	var placement application.OrderPlacement
	found := false
	err := s.db.InScopedTx(ctx, s.scope, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		var sequence int64
		var payload []byte
		err := tx.QueryRow(ctx, `SELECT sequence, payload FROM events WHERE restaurant_id = $1 AND order_id = $2`, s.scope.RestaurantID, orderID).Scan(&sequence, &payload)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		event, err := decodeEvent(payload)
		if err != nil || event.Payload.Order == nil || event.Payload.Order.OrderID != orderID {
			return domain.ErrInvalidRecordedBatch
		}
		placement = application.OrderPlacement{Sequence: sequence, RecordedAt: event.RecordedAt}
		found = true
		return nil
	})
	return placement, found, mapStoreError(err)
}

func (s *EventStore) FindCommand(ctx context.Context, commandID string) (domain.CommandRecord, bool, error) {
	var record domain.CommandRecord
	found := false
	err := s.db.InScopedTx(ctx, s.scope, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		result, exists, err := s.findCommandTx(ctx, tx, commandID)
		if err != nil || !exists {
			return err
		}
		record, found = result.Command, true
		return nil
	})
	return domain.CloneCommandRecord(record), found, mapStoreError(err)
}

func (s *EventStore) FindOrderByProposal(ctx context.Context, proposalID string) (domain.Order, bool, error) {
	var order domain.Order
	found := false
	err := s.db.InScopedTx(ctx, s.scope, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		var payload []byte
		err := tx.QueryRow(ctx, `SELECT payload FROM events WHERE restaurant_id = $1 AND proposal_id = $2`, s.scope.RestaurantID, proposalID).Scan(&payload)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		event, err := decodeEvent(payload)
		if err != nil || event.Payload.Order == nil || event.Payload.Order.ProposalID != proposalID {
			return domain.ErrInvalidRecordedBatch
		}
		order, found = domain.CloneOrder(*event.Payload.Order), true
		return nil
	})
	return order, found, mapStoreError(err)
}

func (s *EventStore) findCommandTx(ctx context.Context, tx pgx.Tx, commandID string) (domain.AppendResult, bool, error) {
	var commandType, fingerprint string
	var first, final int64
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT command_type, fingerprint, first_sequence, final_sequence, result
		FROM commands WHERE restaurant_id = $1 AND command_id = $2`, s.scope.RestaurantID, commandID).Scan(&commandType, &fingerprint, &first, &final, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AppendResult{}, false, nil
	}
	if err != nil {
		return domain.AppendResult{}, false, err
	}
	result, err := decodeResult(raw)
	if err != nil {
		return domain.AppendResult{}, false, domain.ErrInvalidRecordedBatch
	}
	events, err := s.readEventsRangeTx(ctx, tx, first, final)
	if err != nil {
		return domain.AppendResult{}, false, err
	}
	if len(events) == 0 || events[len(events)-1].Sequence != final {
		return domain.AppendResult{}, false, domain.ErrInvalidRecordedBatch
	}
	recorded := domain.RecordedBatch{CommandID: commandID, CommandType: commandType, CommandFingerprint: fingerprint, Events: events, FinalSequence: final}
	if len(events) != int(final-first+1) || events[0].Sequence != first || events[len(events)-1].Sequence != final {
		return domain.AppendResult{}, false, domain.ErrInvalidRecordedBatch
	}
	expected, err := domain.ResultForBatch(recorded)
	if err != nil {
		return domain.AppendResult{}, false, err
	}
	encodedExpected, err := encodeResult(expected)
	if err != nil {
		return domain.AppendResult{}, false, err
	}
	encodedStored, err := encodeResult(result)
	if err != nil || !bytes.Equal(encodedExpected, encodedStored) {
		return domain.AppendResult{}, false, domain.ErrInvalidRecordedBatch
	}
	return domain.AppendResult{Batch: recorded, Command: domain.CommandRecord{CommandID: commandID, CommandType: commandType, CommandFingerprint: fingerprint, Result: result}}, true, nil
}

func mapStoreError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "23505" {
			switch {
			case strings.Contains(pgErr.ConstraintName, "event_id"):
				return fmt.Errorf("%w: duplicate event ID", domain.ErrInvalidEventBatch)
			case strings.Contains(pgErr.ConstraintName, "proposal") || strings.Contains(pgErr.ConstraintName, "order") || strings.Contains(pgErr.ConstraintName, "delivery"):
				return fmt.Errorf("%w: duplicate event identity", domain.ErrInvalidEvent)
			}
		}
		if strings.HasPrefix(pgErr.Code, "53") {
			return fmt.Errorf("warehouse database unavailable: %w", err)
		}
	}
	return err
}

func nullableIdentity(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func cloneAppendResult(value domain.AppendResult) domain.AppendResult {
	value.Batch = domain.CloneRecordedBatch(value.Batch)
	value.Command = domain.CloneCommandRecord(value.Command)
	return value
}

func eventIdentities(event domain.Event) (proposalID, orderID, deliveryID string) {
	if event.Payload.Order != nil {
		proposalID = event.Payload.Order.ProposalID
		orderID = event.Payload.Order.OrderID
	}
	if event.Payload.Delivery != nil {
		deliveryID = event.Payload.Delivery.DeliveryID
	}
	return proposalID, orderID, deliveryID
}
