package warehousepg

import (
	"encoding/json"
	"fmt"

	"github.com/JustIkra/umir-warehouse/internal/adapters/postgres"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

type eventEnvelope struct {
	EventID           string   `json:"eventID"`
	Owner             string   `json:"owner"`
	SchemaVersion     int      `json:"schemaVersion"`
	OrgID             string   `json:"orgID"`
	RestaurantID      string   `json:"restaurantID"`
	AggregateID       string   `json:"aggregateID"`
	AggregateRevision int64    `json:"aggregateRevision"`
	Generation        string   `json:"generation"`
	OccurredAt        string   `json:"occurredAt"`
	CorrelationID     string   `json:"correlationID"`
	CausationID       string   `json:"causationID"`
	Topics            []string `json:"topics"`
}

func outboxEvent(event domain.Event, organizationID, restaurantID, generation string) (postgres.Event, error) {
	topics := topicsForEvent(event.Type)
	if len(topics) == 0 {
		return postgres.Event{}, fmt.Errorf("unsupported Warehouse event type %q", event.Type)
	}
	envelope := eventEnvelope{
		EventID: event.EventID, Owner: "warehouse", SchemaVersion: 1,
		OrgID: organizationID, RestaurantID: restaurantID, AggregateID: restaurantID,
		AggregateRevision: event.Sequence, Generation: generation,
		OccurredAt:    domain.CanonicalBusinessTimeString(event.RecordedAt),
		CorrelationID: event.CommandID, CausationID: event.CommandID,
		Topics: topics,
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return postgres.Event{}, err
	}
	return postgres.Event{
		Exchange: "umir.events", RoutingKey: "warehouse." + string(event.Type),
		Headers: map[string]string{
			"x-umir-schema": "1", "x-umir-generation": generation,
			"x-umir-restaurant-id": restaurantID,
		}, Body: body, OrgID: organizationID, RestaurantID: restaurantID,
		NotBefore: event.RecordedAt,
	}, nil
}

func topicsForEvent(eventType domain.EventType) []string {
	switch eventType {
	case domain.EventProductRegistered, domain.EventDishDefined:
		return []string{"catalog", "menu"}
	case domain.EventDeliveryReceived:
		return []string{"stock", "menu", "analytics"}
	case domain.EventOrderPlaced:
		return []string{"stock", "menu", "orders", "analytics"}
	case domain.EventBatchExpiryPending, domain.EventBatchExpired:
		return []string{"stock", "menu", "analytics"}
	case domain.EventBatchWriteoffConfirmed:
		return []string{"stock", "analytics"}
	default:
		return nil
	}
}
