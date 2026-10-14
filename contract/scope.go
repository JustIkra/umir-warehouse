package contract

import (
	"errors"
	"regexp"
	"strconv"
)

type Scope struct {
	OrganizationID   string
	RestaurantID     string
	RuntimeEpoch     string
	LifecycleVersion string
	TimeZone         string
	SettingsVersion  string
}

type Identity struct {
	Scope       Scope
	Subject     string
	Kind        string
	Operation   string
	Permissions []string
}

var (
	ErrInvalidToken    = errors.New("invalid token")
	ErrUnauthenticated = errors.New("unauthenticated caller")
	ErrForbidden       = errors.New("forbidden request")
	ErrAuthUnavailable = errors.New("authentication keys unavailable")
	ErrRuntimeNotReady = errors.New("restaurant runtime not ready")
	ErrRuntimeReset    = errors.New("runtime epoch changed")
)

var entityID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func ValidEntityID(value string) bool { return entityID.MatchString(value) }
func ValidVersion(value string) bool {
	n, err := strconv.ParseInt(value, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == value
}
func (s Scope) Validate() error {
	if !ValidEntityID(s.OrganizationID) || !ValidEntityID(s.RestaurantID) || !ValidEntityID(s.RuntimeEpoch) || !ValidVersion(s.LifecycleVersion) || !ValidVersion(s.SettingsVersion) || s.TimeZone == "" || s.TimeZone == "Local" || len(s.TimeZone) > 128 {
		return ErrInvalidArgument
	}
	return nil
}

var ErrScopeDeleted = errors.New("restaurant scope deleted")

func (s Scope) ValidateLifecycle() error {
	if !ValidEntityID(s.OrganizationID) || !ValidEntityID(s.RestaurantID) || !ValidEntityID(s.RuntimeEpoch) || !ValidVersion(s.LifecycleVersion) {
		return ErrInvalidArgument
	}
	return nil
}

type RuntimeReadiness struct {
	RestaurantID     string `json:"restaurantID"`
	Service          string `json:"service"`
	Epoch            string `json:"epoch"`
	State            string `json:"state"`
	LifecycleVersion string `json:"lifecycleVersion"`
}
