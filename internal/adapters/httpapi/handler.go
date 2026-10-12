package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
)

type Runtime interface {
	RunExpirations(context.Context) (contract.ExpirationRun, error)
	RegisterProduct(context.Context, contract.RegisterProductRequest) (contract.Product, error)
	DefineDish(context.Context, contract.DefineDishRequest) (contract.Dish, error)
	ReceiveDelivery(context.Context, contract.ReceiveDeliveryRequest) (contract.DeliveryReceipt, error)
	EvaluateOrder(context.Context, contract.EvaluateOrderRequest) (contract.OrderEvaluation, error)
	EvaluateMenu(context.Context) (contract.MenuEvaluation, error)
	CommitAcceptedOrder(context.Context, contract.CommitOrderRequest) (contract.CommitResult, error)
	GetInventory(context.Context) (contract.InventoryView, error)
	GetExpiringBatches(context.Context, time.Time) (contract.ExpiringBatches, error)
	GetHistory(context.Context) (contract.History, error)
	GetProducts(context.Context) (contract.Products, error)
	GetDishCatalog(context.Context) (contract.DishCatalog, error)
	GetDish(context.Context, string) (contract.DishDetails, error)
	FindRecordedCommand(context.Context, string) (contract.CommandRecord, bool, error)
	FindOrderByProposal(context.Context, string) (contract.Order, bool, error)
	FindOrderAttempt(context.Context, string, string) (contract.OrderAttempt, error)
}

type Options struct {
	RequestTimeout  time.Duration
	MaxRequestBytes int64
}

type handlers struct {
	runtime Runtime
	options Options
}

func NewHandlers(runtime Runtime, options Options) (http.Handler, http.Handler, error) {
	if runtime == nil || options.RequestTimeout <= 0 || options.MaxRequestBytes <= 0 || options.MaxRequestBytes > 64<<20 {
		return nil, nil, contract.ErrInvalidArgument
	}
	h := &handlers{runtime: runtime, options: options}
	return handlerFunc(h.servePublic), handlerFunc(h.serveInternal), nil
}

type handlerFunc func(http.ResponseWriter, *http.Request)

func (f handlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) { f(w, r) }

func (h *handlers) servePublic(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.options.RequestTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	switch r.URL.Path {
	case "/expiration-runs":
		if !method(w, r, http.MethodPost) {
			return
		}
		if noQuery(w, r) != nil {
			return
		}
		if hasRequestBody(r) {
			writeError(w, contract.ErrInvalidArgument, true)
			return
		}
		value, err := h.runtime.RunExpirations(ctx)
		if err != nil {
			writeError(w, err, true)
			return
		}
		writeJSON(w, http.StatusOK, wireExpirationRun(value))
	case "/healthz":
		if !method(w, r, http.MethodGet) || noQuery(w, r) != nil {
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Status  string `json:"status"`
			Service string `json:"service"`
		}{"ok", "umir-warehouse"})
	case "/inventory":
		if !method(w, r, http.MethodGet) || noQuery(w, r) != nil {
			return
		}
		value, err := h.runtime.GetInventory(ctx)
		if err != nil {
			writeError(w, err, false)
			return
		}
		writeJSON(w, http.StatusOK, wireInventory(value))
	case "/expiring-batches":
		if !method(w, r, http.MethodGet) {
			return
		}
		values, err := queryValues(r, "cutoffAt")
		if err != nil {
			writeError(w, contract.ErrInvalidArgument, false)
			return
		}
		text, err := requiredQuery(values, "cutoffAt")
		if err != nil {
			writeError(w, contract.ErrInvalidArgument, false)
			return
		}
		cutoff, err := requiredTime(&text)
		if err != nil {
			writeError(w, contract.ErrInvalidArgument, false)
			return
		}
		value, err := h.runtime.GetExpiringBatches(ctx, cutoff)
		if err != nil {
			writeError(w, err, false)
			return
		}
		writeJSON(w, http.StatusOK, wireExpiringBatches(value))
	case "/history":
		if !method(w, r, http.MethodGet) || noQuery(w, r) != nil {
			return
		}
		value, err := h.runtime.GetHistory(ctx)
		if err != nil {
			writeError(w, err, false)
			return
		}
		writeJSON(w, http.StatusOK, wireHistory(value))
	case "/deliveries":
		if !method(w, r, http.MethodPost) || noQuery(w, r) != nil {
			return
		}
		var input deliveryRequest
		if decodeRequest(r, h.options.MaxRequestBytes, &input) != nil {
			writeError(w, contract.ErrInvalidArgument, true)
			return
		}
		request, err := parseDelivery(input)
		if err != nil {
			writeError(w, contract.ErrInvalidArgument, true)
			return
		}
		value, err := h.runtime.ReceiveDelivery(ctx, request)
		if err != nil {
			writeError(w, err, true)
			return
		}
		writeJSON(w, http.StatusOK, wireReceipt(value))
	default:
		writeFailure(w, http.StatusNotFound, "NotFound", "Route was not found.", "unavailable")
	}
}

func hasRequestBody(r *http.Request) bool {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1))
	return len(data) != 0 || err != nil
}

func (h *handlers) serveInternal(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.options.RequestTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	switch r.URL.Path {
	case "/healthz":
		if !method(w, r, http.MethodGet) || noQuery(w, r) != nil {
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Status  string `json:"status"`
			Service string `json:"service"`
		}{"ok", "umir-warehouse"})
	case "/internal/v1/products":
		if r.Method == http.MethodGet {
			if noQuery(w, r) != nil {
				return
			}
			value, err := h.runtime.GetProducts(ctx)
			if err != nil {
				writeError(w, err, false)
				return
			}
			writeJSON(w, http.StatusOK, wireProducts(value))
			return
		}
		if !method(w, r, http.MethodPost) || noQuery(w, r) != nil {
			return
		}
		var input productRequest
		if decodeRequest(r, h.options.MaxRequestBytes, &input) != nil {
			writeError(w, contract.ErrInvalidArgument, true)
			return
		}
		commandID, e1 := requiredString(input.CommandID)
		productID, e2 := requiredString(input.ProductID)
		name, e3 := requiredString(input.Name)
		baseUnit, e4 := requiredString(input.BaseUnit)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			writeError(w, contract.ErrInvalidArgument, true)
			return
		}
		value, err := h.runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: commandID, ProductID: productID, Name: name, BaseUnit: contract.BaseUnit(baseUnit)})
		if err != nil {
			writeError(w, err, true)
			return
		}
		writeJSON(w, http.StatusOK, wireProduct(value))
	case "/internal/v1/dish-catalog":
		if !method(w, r, http.MethodGet) || noQuery(w, r) != nil {
			return
		}
		value, err := h.runtime.GetDishCatalog(ctx)
		if err != nil {
			writeError(w, err, false)
			return
		}
		writeJSON(w, http.StatusOK, wireDishCatalog(value))
	case "/internal/v1/dish":
		if !method(w, r, http.MethodGet) {
			return
		}
		values, err := queryValues(r, "dishID")
		if err != nil {
			writeError(w, contract.ErrInvalidArgument, false)
			return
		}
		id, err := requiredQuery(values, "dishID")
		if err != nil {
			writeError(w, contract.ErrInvalidArgument, false)
			return
		}
		value, err := h.runtime.GetDish(ctx, id)
		if err != nil {
			writeError(w, err, false)
			return
		}
		writeJSON(w, http.StatusOK, wireDishDetails(value))
	case "/internal/v1/dishes":
		if !method(w, r, http.MethodPost) || noQuery(w, r) != nil {
			return
		}
		var input dishRequest
		if decodeRequest(r, h.options.MaxRequestBytes, &input) != nil {
			writeError(w, contract.ErrInvalidArgument, true)
			return
		}
		request, err := parseDish(input)
		if err != nil {
			writeError(w, contract.ErrInvalidArgument, true)
			return
		}
		value, err := h.runtime.DefineDish(ctx, request)
		if err != nil {
			writeError(w, err, true)
			return
		}
		writeJSON(w, http.StatusOK, wireDish(value, true))
	case "/internal/v1/menu-evaluation":
		if !method(w, r, http.MethodGet) || noQuery(w, r) != nil {
			return
		}
		value, err := h.runtime.EvaluateMenu(ctx)
		if err != nil {
			writeError(w, err, false)
			return
		}
		writeJSON(w, http.StatusOK, wireMenuEvaluation(value))
	case "/internal/v1/order-evaluations":
		if !method(w, r, http.MethodPost) || noQuery(w, r) != nil {
			return
		}
		var input orderEvaluationRequest
		if decodeRequest(r, h.options.MaxRequestBytes, &input) != nil {
			writeError(w, contract.ErrInvalidArgument, false)
			return
		}
		positions, err := parsePositions(input.Positions)
		if err != nil {
			writeError(w, contract.ErrInvalidArgument, false)
			return
		}
		value, err := h.runtime.EvaluateOrder(ctx, contract.EvaluateOrderRequest{Positions: positions})
		if err != nil {
			writeError(w, err, false)
			return
		}
		writeJSON(w, http.StatusOK, wireEvaluation(value))
	case "/internal/v1/order-commits":
		if !method(w, r, http.MethodPost) || noQuery(w, r) != nil {
			return
		}
		var input orderCommitRequest
		if decodeRequest(r, h.options.MaxRequestBytes, &input) != nil {
			writeError(w, contract.ErrInvalidArgument, true)
			return
		}
		commandID, e1 := requiredString(input.CommandID)
		proposalID, e2 := requiredString(input.ProposalID)
		positions, e3 := parsePositions(input.AcceptedPositions)
		if e1 != nil || e2 != nil || e3 != nil {
			writeError(w, contract.ErrInvalidArgument, true)
			return
		}
		accepted := make([]contract.AcceptedPosition, len(positions))
		for i, position := range positions {
			accepted[i] = contract.AcceptedPosition(position)
		}
		value, err := h.runtime.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: commandID, ProposalID: proposalID, AcceptedPositions: accepted})
		if err != nil {
			writeError(w, err, true)
			return
		}
		if value.Kind == contract.CommitResultPending {
			if value.Order != nil || value.Evaluation != nil {
				writeFailure(w, http.StatusInternalServerError, "InternalError", "Warehouse returned an invalid commit result.", "unknown")
				return
			}
			writeJSON(w, http.StatusAccepted, commitWire{Kind: contract.CommitResultPending})
			return
		}
		if !validCommitPayload(value) {
			writeFailure(w, http.StatusInternalServerError, "InternalError", "Warehouse returned an invalid commit result.", "unknown")
			return
		}
		writeJSON(w, http.StatusOK, wireCommit(value))
	case "/internal/v1/commands":
		if !method(w, r, http.MethodGet) {
			return
		}
		values, err := queryValues(r, "commandID")
		if err != nil {
			writeError(w, contract.ErrInvalidArgument, false)
			return
		}
		commandID, err := requiredQuery(values, "commandID")
		if err != nil {
			writeError(w, contract.ErrInvalidArgument, false)
			return
		}
		record, found, err := h.runtime.FindRecordedCommand(ctx, commandID)
		if err != nil {
			writeError(w, err, false)
			return
		}
		response := foundCommandWire{Found: found}
		if found {
			wire := wireCommandRecord(record)
			response.Record = &wire
		}
		writeJSON(w, http.StatusOK, response)
	case "/internal/v1/orders":
		if !method(w, r, http.MethodGet) {
			return
		}
		values, err := queryValues(r, "proposalID")
		if err != nil {
			writeError(w, contract.ErrInvalidArgument, false)
			return
		}
		proposalID, err := requiredQuery(values, "proposalID")
		if err != nil {
			writeError(w, contract.ErrInvalidArgument, false)
			return
		}
		order, found, err := h.runtime.FindOrderByProposal(ctx, proposalID)
		if err != nil {
			writeError(w, err, false)
			return
		}
		response := foundOrderWire{Found: found}
		if found {
			wire := wireOrder(order)
			response.Order = &wire
		}
		writeJSON(w, http.StatusOK, response)
	case "/internal/v1/order-attempts":
		if !method(w, r, http.MethodGet) {
			return
		}
		values, err := queryValues(r, "commandID", "proposalID")
		if err != nil {
			writeError(w, contract.ErrInvalidArgument, false)
			return
		}
		commandID, e1 := requiredQuery(values, "commandID")
		proposalID, e2 := requiredQuery(values, "proposalID")
		if e1 != nil || e2 != nil {
			writeError(w, contract.ErrInvalidArgument, false)
			return
		}
		attempt, err := h.runtime.FindOrderAttempt(ctx, commandID, proposalID)
		if err != nil {
			writeError(w, err, false)
			return
		}
		if !validAttemptPayload(attempt) {
			writeFailure(w, http.StatusInternalServerError, "InternalError", "Warehouse returned an invalid attempt state.", "unavailable")
			return
		}
		response := orderAttemptWire{State: attempt.State}
		if attempt.Result != nil {
			result := wireCommit(*attempt.Result)
			response.Result = &result
		}
		if attempt.Error != nil {
			response.Error = &errorWire{Code: attempt.Error.Code, Message: attempt.Error.Message, Outcome: string(attempt.Error.Outcome)}
		}
		writeJSON(w, http.StatusOK, response)
	default:
		writeFailure(w, http.StatusNotFound, "NotFound", "Route was not found.", "unavailable")
	}
}

func validCommitPayload(value contract.CommitResult) bool {
	switch value.Kind {
	case contract.CommitResultOrderCommitted:
		return value.Order != nil && value.Evaluation == nil
	case contract.CommitResultRecheckRequired:
		return value.Evaluation != nil && value.Order == nil
	default:
		return false
	}
}

func validAttemptPayload(value contract.OrderAttempt) bool {
	switch value.State {
	case contract.OrderAttemptUnseen, contract.OrderAttemptPending:
		return value.Result == nil && value.Error == nil
	case contract.OrderAttemptCompleted:
		return value.Result != nil && value.Error == nil && validCommitPayload(*value.Result)
	case contract.OrderAttemptRejected:
		return value.Result == nil && value.Error != nil && value.Error.Outcome == contract.OutcomeNotCommitted
	default:
		return false
	}
}

func method(w http.ResponseWriter, r *http.Request, expected string) bool {
	if r.Method == expected {
		return true
	}
	writeFailure(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "HTTP method is not supported for this route.", "unavailable")
	return false
}

func noQuery(w http.ResponseWriter, r *http.Request) error {
	values, err := queryValues(r)
	if err != nil || len(values) != 0 {
		writeError(w, contract.ErrInvalidArgument, r.Method == http.MethodPost)
		return contract.ErrInvalidArgument
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		writeFailure(w, http.StatusInternalServerError, "InternalError", "Response encoding failed.", "unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(append(data, '\n')); err != nil {
		log.Printf("http response write: %v", err)
	}
}

func writeError(w http.ResponseWriter, err error, write bool) {
	status, code, message, outcome := classifyError(err, write)
	writeFailure(w, status, code, message, outcome)
}

func writeFailure(w http.ResponseWriter, status int, code, message, outcome string) {
	writeJSON(w, status, errorWire{Code: code, Message: message, Outcome: outcome})
}

func classifyError(err error, write bool) (int, string, string, string) {
	outcome := "unavailable"
	if write {
		outcome = "unknown"
	}
	switch {
	case errors.Is(err, contract.ErrUnknownWriteOutcome), errors.Is(err, contract.ErrConcurrentModification):
		return http.StatusInternalServerError, "InternalError", "Warehouse operation outcome is unknown.", "unknown"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		if write {
			return http.StatusGatewayTimeout, "UpstreamTimeout", "Warehouse request exceeded its time budget.", "unknown"
		}
		return http.StatusGatewayTimeout, "UpstreamTimeout", "Warehouse request exceeded its time budget.", "unavailable"
	case errors.Is(err, contract.ErrInventoryUnavailable):
		if write {
			return http.StatusServiceUnavailable, "InventoryUnavailable", "InventoryUnavailable: warehouse state is not available.", "unknown"
		}
		return http.StatusServiceUnavailable, "InventoryUnavailable", "InventoryUnavailable: warehouse state is not available.", "unavailable"
	case errors.Is(err, contract.ErrInvalidArgument):
		return http.StatusBadRequest, "InvalidArgument", "InvalidArgument: request fields are invalid.", definiteOutcome(write)
	case errors.Is(err, contract.ErrCommandIdentityConflict):
		return http.StatusConflict, "CommandIdentityConflict", "CommandIdentityConflict: commandID is bound to another command.", definiteOutcome(write)
	case errors.Is(err, contract.ErrProductNotFound):
		return http.StatusNotFound, "ProductNotFound", "ProductNotFound: product does not exist.", definiteOutcome(write)
	case errors.Is(err, contract.ErrDishNotFound):
		return http.StatusNotFound, "DishNotFound", "DishNotFound: dish does not exist.", definiteOutcome(write)
	case errors.Is(err, contract.ErrProductAlreadyExists):
		return http.StatusConflict, "ProductAlreadyExists", "ProductAlreadyExists: productID is already registered.", definiteOutcome(write)
	case errors.Is(err, contract.ErrDishAlreadyExists):
		return http.StatusConflict, "DishAlreadyExists", "DishAlreadyExists: dishID is already defined.", definiteOutcome(write)
	case errors.Is(err, contract.ErrDeliveryAlreadyReceived):
		return http.StatusConflict, "DeliveryAlreadyReceived", "DeliveryAlreadyReceived: deliveryID was already accepted.", definiteOutcome(write)
	case errors.Is(err, contract.ErrInsufficientInventory):
		return http.StatusConflict, "InsufficientInventory", "InsufficientInventory: the requested operation was refused.", definiteOutcome(write)
	default:
		return http.StatusInternalServerError, "InternalError", "Warehouse operation failed.", outcome
	}
}

func definiteOutcome(write bool) string {
	if write {
		return "not_committed"
	}
	return "unavailable"
}

func parsePositions(input *[]positionRequest) ([]contract.Position, error) {
	if input == nil {
		return nil, errInvalidJSON
	}
	positions := make([]contract.Position, len(*input))
	for index, value := range *input {
		lineNo, e1 := requiredInt64(value.LineNo)
		dishID, e2 := requiredString(value.DishID)
		portions, e3 := requiredInt64(value.Portions)
		if e1 != nil || e2 != nil || e3 != nil || strconv.IntSize == 32 && (lineNo > int64(int(^uint(0)>>1)) || lineNo < int64(-int(^uint(0)>>1)-1)) {
			return nil, errInvalidJSON
		}
		positions[index] = contract.Position{LineNo: lineNo, DishID: dishID, Portions: portions}
	}
	return positions, nil
}

func parseDish(input dishRequest) (contract.DefineDishRequest, error) {
	commandID, e1 := requiredString(input.CommandID)
	dishID, e2 := requiredString(input.DishID)
	name, e3 := requiredString(input.Name)
	if e1 != nil || e2 != nil || e3 != nil || input.Ingredients == nil {
		return contract.DefineDishRequest{}, errInvalidJSON
	}
	result := contract.DefineDishRequest{CommandID: commandID, DishID: dishID, Name: name, Ingredients: make([]contract.Ingredient, len(*input.Ingredients))}
	for index, ingredient := range *input.Ingredients {
		productID, e1 := requiredString(ingredient.ProductID)
		quantity, e2 := requiredInt64(ingredient.Quantity)
		if e1 != nil || e2 != nil {
			return contract.DefineDishRequest{}, errInvalidJSON
		}
		unit := ""
		if ingredient.BaseUnit != nil {
			unit = *ingredient.BaseUnit
			switch contract.BaseUnit(unit) {
			case contract.BaseUnitG, contract.BaseUnitML, contract.BaseUnitPCS:
			default:
				return contract.DefineDishRequest{}, errInvalidJSON
			}
		}
		result.Ingredients[index] = contract.Ingredient{ProductID: productID, Quantity: contract.Quantity(quantity), BaseUnit: contract.BaseUnit(unit)}
	}
	return result, nil
}

func parseDelivery(input deliveryRequest) (contract.ReceiveDeliveryRequest, error) {
	commandID, e1 := requiredString(input.CommandID)
	deliveryID, e2 := requiredString(input.DeliveryID)
	number, e3 := requiredString(input.Number)
	createdAt, e4 := requiredTime(input.CreatedAt)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || input.Lines == nil {
		return contract.ReceiveDeliveryRequest{}, errInvalidJSON
	}
	result := contract.ReceiveDeliveryRequest{CommandID: commandID, DeliveryID: deliveryID, Number: number, CreatedAt: createdAt, Lines: make([]contract.DeliveryLine, len(*input.Lines))}
	for index, line := range *input.Lines {
		productID, e1 := requiredString(line.ProductID)
		manufacturedAt, e2 := requiredTime(line.ManufacturedAt)
		receivedAt, e3 := requiredTime(line.ReceivedAt)
		quantity, e4 := requiredInt64(line.Quantity)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || line.ShelfLife == nil {
			return contract.ReceiveDeliveryRequest{}, errInvalidJSON
		}
		lifeValue, e5 := requiredInt64(line.ShelfLife.Value)
		lifeUnit, e6 := requiredString(line.ShelfLife.Unit)
		if e5 != nil || e6 != nil {
			return contract.ReceiveDeliveryRequest{}, errInvalidJSON
		}
		result.Lines[index] = contract.DeliveryLine{ProductID: productID, ManufacturedAt: manufacturedAt, ShelfLife: contract.ShelfLife{Value: lifeValue, Unit: contract.ShelfLifeUnit(lifeUnit)}, ReceivedAt: receivedAt, Quantity: contract.Quantity(quantity)}
	}
	return result, nil
}
