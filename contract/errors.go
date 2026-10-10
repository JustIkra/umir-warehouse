package contract

import "errors"

var ErrInvalidArgument = errors.New("invalid argument")
var ErrCommandIdentityConflict = errors.New("command identity conflict")
var ErrDeliveryAlreadyReceived = errors.New("delivery already received")
var ErrProductNotFound = errors.New("product not found")
var ErrDishNotFound = errors.New("dish not found")
var ErrProductAlreadyExists = errors.New("product already exists")
var ErrDishAlreadyExists = errors.New("dish already exists")
var ErrInventoryUnavailable = errors.New("inventory unavailable")
var ErrInsufficientInventory = errors.New("insufficient inventory")
var ErrConcurrentModification = errors.New("concurrent modification")
var ErrUnknownWriteOutcome = errors.New("unknown write outcome")
var ErrNotDue = errors.New("batch is not due")
var ErrUnsupportedSchema = errors.New("unsupported schema")
