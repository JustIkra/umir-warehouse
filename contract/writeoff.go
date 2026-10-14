package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
	"unicode/utf8"
)

type ConfirmWriteoffRequest struct {
	Scope       Scope
	CommandID   string
	BatchID     string
	ActorUserID string
}

type WriteoffPreparationError struct {
	Cause            error
	CommittedVersion int64
}

func (e *WriteoffPreparationError) Error() string { return e.Cause.Error() }
func (e *WriteoffPreparationError) Unwrap() error { return e.Cause }

type WriteoffObservation struct {
	State        string
	Fingerprint  string
	Result       *WriteoffResult
	EvaluatedAt  time.Time
	StateVersion int64
}

func WriteoffFingerprint(scope Scope, batchID string) (string, error) {
	if scope.Validate() != nil || batchID == "" || !utf8.ValidString(batchID) {
		return "", ErrInvalidArgument
	}
	value := `{"operation":"writeoff.confirm","orgID":` + canonicalString(scope.OrganizationID) + `,"restaurantID":` + canonicalString(scope.RestaurantID) + `,"runtimeEpoch":` + canonicalString(scope.RuntimeEpoch) + `,"batchID":` + canonicalString(batchID) + `}`
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:]), nil
}
func canonicalString(value string) string {
	buffer := make([]byte, 0, len(value)+2)
	buffer = append(buffer, '"')
	for _, r := range value {
		switch r {
		case '"', '\\':
			buffer = append(buffer, '\\', byte(r))
		case '\b':
			buffer = append(buffer, '\\', 'b')
		case '\t':
			buffer = append(buffer, '\\', 't')
		case '\n':
			buffer = append(buffer, '\\', 'n')
		case '\f':
			buffer = append(buffer, '\\', 'f')
		case '\r':
			buffer = append(buffer, '\\', 'r')
		default:
			if r < 32 {
				buffer = append(buffer, '\\', 'u', '0', '0', "0123456789abcdef"[r>>4], "0123456789abcdef"[r&15])
			} else {
				buffer = utf8.AppendRune(buffer, r)
			}
		}
	}
	buffer = append(buffer, '"')
	return string(buffer)
}
