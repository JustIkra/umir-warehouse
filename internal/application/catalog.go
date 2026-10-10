package application

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func (o *Owner) RegisterProduct(ctx context.Context, request contract.RegisterProductRequest) (contract.Product, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if request.CommandID == "" || isReservedCommandID(request.CommandID) {
		return contract.Product{}, contract.ErrInvalidArgument
	}
	fingerprint := fingerprintJSON("RegisterProduct", struct {
		ProductID string
		Name      string
		BaseUnit  contract.BaseUnit
	}{ProductID: request.ProductID, Name: request.Name, BaseUnit: request.BaseUnit})
	for {
		record, found, err := o.findCommand(ctx, request.CommandID, "RegisterProduct", fingerprint)
		if err != nil {
			return contract.Product{}, err
		}
		if found {
			return recordedProduct(record, fingerprint)
		}
		if err := o.syncProjectionLocked(ctx); err != nil {
			return contract.Product{}, inventoryUnavailable(err)
		}
		if request.ProductID == "" || request.Name == "" || !validContractUnit(request.BaseUnit) {
			return contract.Product{}, contract.ErrInvalidArgument
		}
		if _, exists := o.state.Products[request.ProductID]; exists {
			return contract.Product{}, contract.ErrProductAlreadyExists
		}
		product := domain.Product{ProductID: request.ProductID, Name: request.Name, BaseUnit: domain.BaseUnit(request.BaseUnit)}
		event := domain.Event{EventID: o.newID(), RecordedAt: domain.CanonicalBusinessTime(o.now()), Type: domain.EventProductRegistered, Payload: domain.EventPayload{Product: &product}}
		if event.EventID == "" {
			return contract.Product{}, contract.ErrInvalidArgument
		}
		record, found, err = o.appendOwnerCommandLocked(ctx, domain.CommandBatch{ExpectedVersion: o.state.StateVersion, CommandID: request.CommandID, CommandType: "RegisterProduct", CommandFingerprint: fingerprint, Events: []domain.Event{event}})
		if errors.Is(err, domain.ErrConcurrentModification) {
			continue
		}
		if err != nil {
			return contract.Product{}, err
		}
		if !found {
			return contract.Product{}, domain.ErrUnknownWriteOutcome
		}
		return recordedProduct(record, fingerprint)
	}
}

func (o *Owner) DefineDish(ctx context.Context, request contract.DefineDishRequest) (contract.Dish, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if request.CommandID == "" || isReservedCommandID(request.CommandID) {
		return contract.Dish{}, contract.ErrInvalidArgument
	}
	fingerprint := fingerprintJSON("DefineDish", struct {
		DishID      string
		Name        string
		Ingredients []contract.Ingredient
	}{DishID: request.DishID, Name: request.Name, Ingredients: request.Ingredients})
	for {
		record, found, err := o.findCommand(ctx, request.CommandID, "DefineDish", fingerprint)
		if err != nil {
			return contract.Dish{}, err
		}
		if found {
			if err := o.ensureDishProductsLocked(ctx, record.Result.Dish); err != nil {
				return contract.Dish{}, inventoryUnavailable(err)
			}
			return recordedDish(record, fingerprint, o.state)
		}
		if err := o.syncProjectionLocked(ctx); err != nil {
			return contract.Dish{}, inventoryUnavailable(err)
		}
		if request.DishID == "" || request.Name == "" || len(request.Ingredients) == 0 {
			return contract.Dish{}, contract.ErrInvalidArgument
		}
		if _, exists := o.state.Dishes[request.DishID]; exists {
			return contract.Dish{}, contract.ErrDishAlreadyExists
		}
		ingredients := make([]domain.Ingredient, len(request.Ingredients))
		seen := make(map[string]struct{}, len(request.Ingredients))
		for index, ingredient := range request.Ingredients {
			if ingredient.ProductID == "" || ingredient.Quantity <= 0 {
				return contract.Dish{}, contract.ErrInvalidArgument
			}
			if ingredient.BaseUnit != "" && !validContractUnit(ingredient.BaseUnit) {
				return contract.Dish{}, contract.ErrInvalidArgument
			}
			if _, exists := seen[ingredient.ProductID]; exists {
				return contract.Dish{}, contract.ErrInvalidArgument
			}
			seen[ingredient.ProductID] = struct{}{}
			product, exists := o.state.Products[ingredient.ProductID]
			if !exists {
				return contract.Dish{}, contract.ErrProductNotFound
			}
			if ingredient.BaseUnit != "" && domain.BaseUnit(ingredient.BaseUnit) != product.BaseUnit {
				return contract.Dish{}, contract.ErrInvalidArgument
			}
			ingredients[index] = domain.Ingredient{ProductID: ingredient.ProductID, Quantity: domain.Quantity(ingredient.Quantity)}
		}
		dish := domain.Dish{DishID: request.DishID, Name: request.Name, Ingredients: ingredients}
		event := domain.Event{EventID: o.newID(), RecordedAt: domain.CanonicalBusinessTime(o.now()), Type: domain.EventDishDefined, Payload: domain.EventPayload{Dish: &dish}}
		if event.EventID == "" {
			return contract.Dish{}, contract.ErrInvalidArgument
		}
		record, found, err = o.appendOwnerCommandLocked(ctx, domain.CommandBatch{ExpectedVersion: o.state.StateVersion, CommandID: request.CommandID, CommandType: "DefineDish", CommandFingerprint: fingerprint, Events: []domain.Event{event}})
		if errors.Is(err, domain.ErrConcurrentModification) {
			continue
		}
		if err != nil {
			return contract.Dish{}, err
		}
		if !found {
			return contract.Dish{}, domain.ErrUnknownWriteOutcome
		}
		return recordedDish(record, fingerprint, o.state)
	}
}

func (o *Owner) GetProducts(ctx context.Context) (contract.Products, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.syncProjectionLocked(ctx); err != nil {
		return contract.Products{}, inventoryUnavailable(err)
	}
	ids := make([]string, 0, len(o.state.Products))
	for id := range o.state.Products {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]contract.Product, 0, len(ids))
	for _, id := range ids {
		items = append(items, toContractProduct(o.state.Products[id]))
	}
	return contract.Products{EvaluatedAt: domain.CanonicalBusinessTime(o.now()), StateVersion: o.state.StateVersion, Items: items}, nil
}

func (o *Owner) GetDishCatalog(ctx context.Context) (contract.DishCatalog, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.syncProjectionLocked(ctx); err != nil {
		return contract.DishCatalog{}, inventoryUnavailable(err)
	}
	ids := make([]string, 0, len(o.state.Dishes))
	for id := range o.state.Dishes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]contract.Dish, 0, len(ids))
	for _, id := range ids {
		items = append(items, toContractDish(o.state, o.state.Dishes[id]))
	}
	return contract.DishCatalog{EvaluatedAt: domain.CanonicalBusinessTime(o.now()), StateVersion: o.state.StateVersion, Items: items}, nil
}

func (o *Owner) GetDish(ctx context.Context, dishID string) (contract.Dish, error) {
	if dishID == "" {
		return contract.Dish{}, contract.ErrInvalidArgument
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.syncProjectionLocked(ctx); err != nil {
		return contract.Dish{}, inventoryUnavailable(err)
	}
	dish, exists := o.state.Dishes[dishID]
	if !exists {
		return contract.Dish{}, contract.ErrDishNotFound
	}
	return toContractDish(o.state, dish), nil
}

func (o *Owner) findCommand(ctx context.Context, commandID, commandType, fingerprint string) (domain.CommandRecord, bool, error) {
	record, found, err := o.events.FindCommand(ctx, commandID)
	if err != nil {
		if errors.Is(err, domain.ErrCommandIdentityConflict) {
			return domain.CommandRecord{}, false, contract.ErrCommandIdentityConflict
		}
		return domain.CommandRecord{}, false, err
	}
	if !found {
		return domain.CommandRecord{}, false, nil
	}
	if record.CommandType != commandType || record.CommandFingerprint != fingerprint {
		return domain.CommandRecord{}, false, contract.ErrCommandIdentityConflict
	}
	return domain.CloneCommandRecord(record), true, nil
}

func (o *Owner) appendOwnerCommandLocked(ctx context.Context, command domain.CommandBatch) (domain.CommandRecord, bool, error) {
	result, err := o.events.AppendBatch(ctx, command)
	if errors.Is(err, domain.ErrUnknownWriteOutcome) {
		o.pending = cloneCommandBatch(command)
		record, found, resolveErr := o.resolvePendingLocked(ctx)
		if found {
			return record, true, nil
		}
		if resolveErr != nil {
			if errors.Is(resolveErr, domain.ErrCommandIdentityConflict) {
				return domain.CommandRecord{}, false, contract.ErrCommandIdentityConflict
			}
			return domain.CommandRecord{}, false, resolveErr
		}
		return domain.CommandRecord{}, false, domain.ErrUnknownWriteOutcome
	}
	if err != nil {
		if errors.Is(err, domain.ErrCommandIdentityConflict) {
			return domain.CommandRecord{}, false, contract.ErrCommandIdentityConflict
		}
		return domain.CommandRecord{}, false, err
	}
	if result.Command.CommandID != command.CommandID || result.Command.CommandType != command.CommandType || result.Command.CommandFingerprint != command.CommandFingerprint {
		o.usable = false
		return domain.CommandRecord{}, false, domain.ErrInvalidRecordedBatch
	}
	if err := o.applyCommittedBatch(result.Batch); err != nil {
		o.usable = false
	}
	return domain.CloneCommandRecord(result.Command), true, nil
}

func recordedProduct(record domain.CommandRecord, fingerprint string) (contract.Product, error) {
	if record.CommandType != "RegisterProduct" || record.CommandFingerprint != fingerprint {
		return contract.Product{}, contract.ErrCommandIdentityConflict
	}
	if record.Result.Kind != domain.CommandResultProductRegistered || record.Result.Product == nil {
		return contract.Product{}, domain.ErrInvalidRecordedBatch
	}
	return toContractProduct(*record.Result.Product), nil
}

func recordedDish(record domain.CommandRecord, fingerprint string, state domain.InventoryState) (contract.Dish, error) {
	if record.CommandType != "DefineDish" || record.CommandFingerprint != fingerprint {
		return contract.Dish{}, contract.ErrCommandIdentityConflict
	}
	if record.Result.Kind != domain.CommandResultDishDefined || record.Result.Dish == nil {
		return contract.Dish{}, domain.ErrInvalidRecordedBatch
	}
	return toContractDish(state, *record.Result.Dish), nil
}

func (o *Owner) ensureDishProductsLocked(ctx context.Context, dish *domain.Dish) error {
	if dish == nil {
		return domain.ErrInvalidRecordedBatch
	}
	for _, ingredient := range dish.Ingredients {
		if _, exists := o.state.Products[ingredient.ProductID]; !exists {
			return o.syncProjectionLocked(ctx)
		}
	}
	return nil
}

func toContractProduct(value domain.Product) contract.Product {
	return contract.Product{ProductID: value.ProductID, Name: value.Name, BaseUnit: contract.BaseUnit(value.BaseUnit)}
}

func toContractDish(state domain.InventoryState, value domain.Dish) contract.Dish {
	ingredients := make([]contract.Ingredient, 0, len(value.Ingredients))
	for _, ingredient := range value.Ingredients {
		product := state.Products[ingredient.ProductID]
		ingredients = append(ingredients, contract.Ingredient{ProductID: ingredient.ProductID, Quantity: contract.Quantity(ingredient.Quantity), BaseUnit: contract.BaseUnit(product.BaseUnit)})
	}
	return contract.Dish{DishID: value.DishID, Name: value.Name, Ingredients: ingredients}
}

func validContractUnit(unit contract.BaseUnit) bool {
	switch unit {
	case contract.BaseUnitG, contract.BaseUnitML, contract.BaseUnitPCS:
		return true
	default:
		return false
	}
}

func isReservedCommandID(commandID string) bool {
	return strings.HasPrefix(commandID, "expiration/")
}

func fingerprintJSON(commandType string, value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return domain.Fingerprint(commandType, nil)
	}
	return domain.Fingerprint(commandType, encoded)
}

func canonicalCommandTime(value time.Time) string {
	return domain.CanonicalBusinessTimeString(value)
}
