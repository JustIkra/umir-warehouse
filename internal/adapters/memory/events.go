package memory

import (
	"context"
	"math"
	"sync"

	"github.com/JustIkra/umir-warehouse/internal/domain"
)

type EventStore struct {
	mu        sync.RWMutex
	version   int64
	batches   []domain.RecordedBatch
	commands  map[string]storedCommand
	eventIDs  map[string]struct{}
	proposals map[string]domain.Order
}

type storedCommand struct {
	batch  domain.RecordedBatch
	record domain.CommandRecord
}

func NewEventStore() *EventStore {
	return &EventStore{
		commands:  map[string]storedCommand{},
		eventIDs:  map[string]struct{}{},
		proposals: map[string]domain.Order{},
	}
}

func (s *EventStore) AppendBatch(ctx context.Context, batch domain.CommandBatch) (domain.AppendResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.AppendResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.AppendResult{}, err
	}
	if existing, ok := s.commands[batch.CommandID]; ok {
		if batch.CommandID == "" || existing.record.CommandType != batch.CommandType || existing.record.CommandFingerprint != batch.CommandFingerprint {
			return domain.AppendResult{}, domain.ErrCommandIdentityConflict
		}
		return domain.AppendResult{Batch: domain.CloneRecordedBatch(existing.batch), Command: domain.CloneCommandRecord(existing.record), Replayed: true}, nil
	}
	if batch.ExpectedVersion != s.version {
		return domain.AppendResult{}, domain.ErrConcurrentModification
	}
	if err := validateCommandBatch(batch); err != nil {
		return domain.AppendResult{}, err
	}
	if int64(len(batch.Events)) > math.MaxInt64-s.version {
		return domain.AppendResult{}, domain.ErrInvalidEventBatch
	}
	for i, event := range batch.Events {
		if event.Sequence != 0 && event.Sequence != s.version+int64(i)+1 {
			return domain.AppendResult{}, domain.ErrInvalidEventBatch
		}
		if _, exists := s.eventIDs[event.EventID]; exists {
			return domain.AppendResult{}, domain.ErrInvalidEventBatch
		}
	}
	if err := ctx.Err(); err != nil {
		return domain.AppendResult{}, err
	}
	recorded := domain.RecordedBatch{
		CommandID:          batch.CommandID,
		CommandType:        batch.CommandType,
		CommandFingerprint: batch.CommandFingerprint,
		Events:             make([]domain.Event, len(batch.Events)),
		FinalSequence:      s.version + int64(len(batch.Events)),
	}
	for i, source := range batch.Events {
		event := domain.CloneEvent(source)
		event.StreamID = domain.InventoryStreamID
		event.Sequence = s.version + int64(i) + 1
		event.SchemaVersion = domain.EventSchemaVersion(event)
		event.CommandID = batch.CommandID
		event.CommandFingerprint = batch.CommandFingerprint
		event.RecordedAt = event.RecordedAt.Round(0)
		if err := domain.ValidateRecordedEvent(event, recorded, s.version+int64(i)+1); err != nil {
			return domain.AppendResult{}, err
		}
		recorded.Events[i] = event
	}
	result, err := domain.ResultForBatch(recorded)
	if err != nil {
		return domain.AppendResult{}, err
	}
	proposalID := ""
	if result.Order != nil {
		proposalID = result.Order.ProposalID
		if _, exists := s.proposals[proposalID]; exists {
			return domain.AppendResult{}, domain.ErrInvalidEvent
		}
	}
	record := domain.CommandRecord{
		CommandID:          batch.CommandID,
		CommandType:        batch.CommandType,
		CommandFingerprint: batch.CommandFingerprint,
		Result:             result,
	}
	storedBatch := domain.CloneRecordedBatch(recorded)
	storedRecord := domain.CloneCommandRecord(record)
	s.batches = append(s.batches, storedBatch)
	s.commands[batch.CommandID] = storedCommand{batch: storedBatch, record: storedRecord}
	for _, event := range recorded.Events {
		s.eventIDs[event.EventID] = struct{}{}
	}
	if proposalID != "" {
		s.proposals[proposalID] = domain.CloneOrder(*result.Order)
	}
	s.version = recorded.FinalSequence
	return domain.AppendResult{Batch: domain.CloneRecordedBatch(storedBatch), Command: domain.CloneCommandRecord(storedRecord)}, nil
}

func (s *EventStore) ReadBatches(ctx context.Context, afterSequence int64) ([]domain.RecordedBatch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if afterSequence < 0 {
		return nil, domain.ErrInvalidRecordedBatch
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if afterSequence > 0 && afterSequence < s.version {
		boundary := false
		for _, batch := range s.batches {
			if batch.FinalSequence == afterSequence {
				boundary = true
				break
			}
		}
		if !boundary {
			return nil, domain.ErrInvalidRecordedBatch
		}
	}
	result := make([]domain.RecordedBatch, 0, len(s.batches))
	for _, batch := range s.batches {
		if batch.FinalSequence > afterSequence {
			result = append(result, domain.CloneRecordedBatch(batch))
		}
	}
	return result, nil
}

func (s *EventStore) FindCommand(ctx context.Context, commandID string) (domain.CommandRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.CommandRecord{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	command, ok := s.commands[commandID]
	if !ok {
		return domain.CommandRecord{}, false, nil
	}
	return domain.CloneCommandRecord(command.record), true, nil
}

func (s *EventStore) FindOrderByProposal(ctx context.Context, proposalID string) (domain.Order, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Order{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	order, ok := s.proposals[proposalID]
	if !ok {
		return domain.Order{}, false, nil
	}
	return domain.CloneOrder(order), true, nil
}

func (s *EventStore) Version(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version, nil
}

func validateCommandBatch(batch domain.CommandBatch) error {
	if batch.ExpectedVersion < 0 || batch.CommandID == "" || batch.CommandType == "" || batch.CommandFingerprint == "" || len(batch.Events) == 0 {
		return domain.ErrInvalidEventBatch
	}
	seen := map[string]struct{}{}
	for _, event := range batch.Events {
		if event.EventID == "" || event.RecordedAt.IsZero() {
			return domain.ErrInvalidEventBatch
		}
		expectedSchema := domain.EventSchemaVersion(event)
		if event.SchemaVersion != 0 && event.SchemaVersion != expectedSchema {
			return domain.ErrUnsupportedSchema
		}
		if event.StreamID != "" && event.StreamID != domain.InventoryStreamID || event.Sequence < 0 || event.CommandID != "" && event.CommandID != batch.CommandID || event.CommandFingerprint != "" && event.CommandFingerprint != batch.CommandFingerprint {
			return domain.ErrInvalidEventBatch
		}
		if _, offset := event.RecordedAt.Zone(); offset != 3*60*60 {
			return domain.ErrInvalidBusinessTime
		}
		if _, exists := seen[event.EventID]; exists {
			return domain.ErrInvalidEventBatch
		}
		seen[event.EventID] = struct{}{}
	}
	return nil
}
