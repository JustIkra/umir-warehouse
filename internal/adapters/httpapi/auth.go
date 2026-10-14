package httpapi

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/zonedata"
)

type SigningKeys interface {
	Key(context.Context, string) (ed25519.PublicKey, error)
}
type Verifier interface {
	Validate(context.Context, string, string, string, string, string) (contract.Identity, error)
}
type JWTOptions struct {
	Now  func() time.Time
	Keys SigningKeys
}
type JWTVerifier struct{ options JWTOptions }

func NewJWTVerifier(options JWTOptions) (*JWTVerifier, error) {
	if options.Now == nil || options.Keys == nil {
		return nil, contract.ErrInvalidArgument
	}
	return &JWTVerifier{options}, nil
}

type jwtHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
	KeyID     string `json:"kid"`
}
type jwtClaims struct {
	Issuer            string   `json:"iss"`
	Audiences         []string `json:"aud"`
	IssuedAt          *int64   `json:"iat"`
	NotBefore         *int64   `json:"nbf"`
	Expires           *int64   `json:"exp"`
	ID                string   `json:"jti"`
	Operation         string   `json:"operation"`
	Permissions       []string `json:"permissions"`
	Kind              string   `json:"kind"`
	Subject           string   `json:"sub"`
	SessionID         string   `json:"sid"`
	SessionVersion    string   `json:"session_version"`
	CredentialVersion string   `json:"credential_version"`
	AccessVersion     string   `json:"access_version"`
	OrganizationID    string   `json:"org_id"`
	TenantID          string   `json:"tenant_id"`
	TimeZone          string   `json:"time_zone"`
	SettingsVersion   string   `json:"settings_version"`
	LifecycleVersion  string   `json:"lifecycle_version"`
	RuntimeEpoch      string   `json:"runtime_epoch"`
	AuthorizedParty   string   `json:"azp"`
}

func decodeJWTObject(segment string, target any) error {
	data, err := base64.RawURLEncoding.Strict().DecodeString(segment)
	if err != nil || !utf8.Valid(data) || rejectDuplicateFields(data) != nil {
		return contract.ErrInvalidToken
	}
	if err := validateJWTKeys(data, target); err != nil {
		return err
	}
	return nil
}
func validateJWTKeys(data []byte, target any) error {
	var raw any
	if json.Unmarshal(data, &raw) != nil || validateJSONKeys(raw, reflect.TypeOf(target)) != nil {
		return contract.ErrInvalidToken
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return contract.ErrInvalidToken
	}
	return nil
}

var audiences = []string{"umir-warehouse", "umir-serving", "umir-gateway", "umir-feedback"}
var capabilities = []string{"catalog.read", "catalog.write", "stock.read", "delivery.create", "analytics.read", "writeoff.confirm", "menu.read", "recommendations.read", "order.prepare", "order.confirm", "order.read", "feedback.submit", "org.read", "org.settings.write", "restaurant.create", "restaurant.delete", "org.delete", "membership.manage", "invitation.manage", "org.summary.read", "runtime.register", "runtime.close", "expiry.due", "consumer.menu.evaluate", "consumer.order.evaluate", "consumer.order.commit", "consumer.order.resolve", "feedback.anonymous.submit", "feedback.read", "feedback.export"}

func uniqueAllowed(values, allowed []string, max int) bool {
	if len(values) < 1 || len(values) > max {
		return false
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if seen[value] || !slices.Contains(allowed, value) {
			return false
		}
		seen[value] = true
	}
	return true
}
func (v *JWTVerifier) Validate(ctx context.Context, token, caller, tenant, operation, epoch string) (contract.Identity, error) {
	if len(token) > 16384 {
		return contract.Identity{}, contract.ErrInvalidToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return contract.Identity{}, contract.ErrInvalidToken
	}
	var header jwtHeader
	var c jwtClaims
	if decodeJWTObject(parts[0], &header) != nil || header.Algorithm != "EdDSA" || header.Type != "JWT" || header.KeyID == "" || decodeJWTObject(parts[1], &c) != nil {
		return contract.Identity{}, contract.ErrInvalidToken
	}
	key, err := v.options.Keys.Key(ctx, header.KeyID)
	if err != nil {
		if errors.Is(err, contract.ErrInvalidToken) {
			return contract.Identity{}, err
		}
		return contract.Identity{}, errors.Join(contract.ErrAuthUnavailable, err)
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), signature) {
		return contract.Identity{}, contract.ErrInvalidToken
	}
	clock := v.options.Now()
	if clock.IsZero() {
		return contract.Identity{}, contract.ErrAuthUnavailable
	}
	now := clock.Unix()
	if c.Issuer != "urn:umir:auth" || !uniqueAllowed(c.Audiences, audiences, 2) || !slices.Contains(c.Audiences, "umir-warehouse") || c.IssuedAt == nil || c.NotBefore == nil || c.Expires == nil || *c.IssuedAt < 0 || *c.IssuedAt > now || *c.NotBefore != *c.IssuedAt || *c.Expires-*c.IssuedAt != 60 || *c.Expires <= now || !contract.ValidEntityID(c.ID) || !contract.ValidEntityID(c.OrganizationID) || !contract.ValidEntityID(c.TenantID) || !contract.ValidVersion(c.LifecycleVersion) || !uniqueAllowed(c.Permissions, capabilities, 40) || c.Operation == "" {
		return contract.Identity{}, contract.ErrInvalidToken
	}
	if !validClaimsShape(parts[1], c.Kind) {
		return contract.Identity{}, contract.ErrInvalidToken
	}
	switch c.Kind {
	case "user":
		if !contract.ValidEntityID(c.Subject) || !contract.ValidEntityID(c.SessionID) || !contract.ValidVersion(c.SessionVersion) || !contract.ValidVersion(c.CredentialVersion) || !contract.ValidVersion(c.AccessVersion) || !contract.ValidVersion(c.SettingsVersion) || (c.TimeZone == "" || c.TimeZone == "Local" || len(c.TimeZone) > 128) || c.AuthorizedParty != "umir-gateway" || c.RuntimeEpoch != "" {
			return contract.Identity{}, contract.ErrInvalidToken
		}
		if _, err := zonedata.Load(c.TimeZone); err != nil {
			return contract.Identity{}, contract.ErrInvalidToken
		}
	case "service":
		if !contract.ValidEntityID(c.RuntimeEpoch) || c.SessionID != "" || c.SessionVersion != "" || c.CredentialVersion != "" || c.AccessVersion != "" || c.TimeZone != "" || c.SettingsVersion != "" || ((c.Subject != "service:auth" || c.AuthorizedParty != "umir-auth") && (c.Subject != "service:gateway" || c.AuthorizedParty != "umir-gateway")) {
			return contract.Identity{}, contract.ErrInvalidToken
		}
	default:
		return contract.Identity{}, contract.ErrInvalidToken
	}
	if c.TenantID != tenant {
		return contract.Identity{}, contract.ErrForbidden
	}
	policy, ok := operationPolicy(operation, caller)
	if !ok || c.Kind != policy.kind || !slices.Contains(policy.operations, c.Operation) || !slices.Contains(c.Permissions, policy.capability) {
		return contract.Identity{}, contract.ErrForbidden
	}
	if c.Kind == "service" && (c.AuthorizedParty != caller || len(c.Audiences) != 1 || len(c.Permissions) != 1) {
		return contract.Identity{}, contract.ErrForbidden
	}
	if caller == "umir-serving" && (!slices.Contains(c.Audiences, "umir-serving") || len(c.Audiences) != 2) {
		return contract.Identity{}, contract.ErrForbidden
	}
	if caller == "umir-gateway" && c.Kind == "user" && len(c.Audiences) != 1 {
		return contract.Identity{}, contract.ErrForbidden
	}
	if c.Kind == "service" && epoch != "" && c.RuntimeEpoch != epoch {
		return contract.Identity{}, contract.ErrRuntimeReset
	}
	return contract.Identity{Scope: contract.Scope{OrganizationID: c.OrganizationID, RestaurantID: c.TenantID, RuntimeEpoch: epoch, LifecycleVersion: c.LifecycleVersion, TimeZone: c.TimeZone, SettingsVersion: c.SettingsVersion}, Subject: c.Subject, Kind: c.Kind, Operation: c.Operation, Permissions: append([]string(nil), c.Permissions...)}, nil
}

type authorizationPolicy struct {
	kind, capability string
	operations       []string
}

func operationPolicy(operation, caller string) (authorizationPolicy, bool) {
	p := authorizationPolicy{kind: "user"}
	if caller == "umir-auth" && (operation == "warehouseRegister" || operation == "warehouseClose") {
		p.kind = "service"
		p.capability = "runtime.register"
		if operation == "warehouseClose" {
			p.capability = "runtime.close"
		}
		p.operations = []string{p.capability}
		return p, true
	}
	if caller == "umir-gateway" && operation == "dueExpiry" {
		return authorizationPolicy{"service", "expiry.due", []string{"expiry.due"}}, true
	}
	if caller == "umir-serving" {
		switch operation {
		case "privateproducts":
			p.capability = "catalog.read"
			p.operations = []string{"servingProducts"}
		case "privatedishCatalog":
			p.capability = "catalog.read"
			p.operations = []string{"servingDishes"}
		case "privatedishDetails":
			p.capability = "catalog.read"
			p.operations = []string{"servingDish"}
		case "consumerMenu":
			p.capability = "consumer.menu.evaluate"
			p.operations = []string{"menu", "stopList", "recommendations"}
		case "consumerEvaluate":
			p.capability = "consumer.order.evaluate"
			p.operations = []string{"prepareOrder", "confirmOrder"}
		case "consumerCommit":
			p.capability = "consumer.order.commit"
			p.operations = []string{"confirmOrder"}
		case "consumerCommand", "consumerOrder", "consumerAttempt":
			p.capability = "consumer.order.resolve"
			p.operations = []string{"confirmOrder", "resolveOrder"}
		default:
			return p, false
		}
		return p, true
	}
	if caller != "umir-gateway" {
		return p, false
	}
	switch operation {
	case "privateproducts":
		p.capability = "catalog.read"
		p.operations = []string{"products"}
	case "privatedishCatalog":
		p.capability = "catalog.read"
		p.operations = []string{"dishCatalog"}
	case "privatedishDetails":
		p.capability = "catalog.read"
		p.operations = []string{"dishDetails"}
	case "privateregisterProduct":
		p.capability = "catalog.write"
		p.operations = []string{"registerProduct"}
	case "privatedefineDish":
		p.capability = "catalog.write"
		p.operations = []string{"defineDish"}
	case "privatereceiveDelivery":
		p.capability = "delivery.create"
		p.operations = []string{"receiveDelivery"}
	case "privateinventory":
		p.capability = "stock.read"
		p.operations = []string{"inventory"}
	case "privateexpiringBatches":
		p.capability = "stock.read"
		p.operations = []string{"expiringBatches"}
	case "privateProductBatches":
		p.capability = "stock.read"
		p.operations = []string{"productBatches"}
	case "privatehistory":
		p.capability = "stock.read"
		p.operations = []string{"history"}
	case "privatePendingWriteoffs":
		p.capability = "stock.read"
		p.operations = []string{"pendingWriteoffs"}
	case "privateconfirmWriteoff":
		p.capability = "writeoff.confirm"
		p.operations = []string{"confirmWriteoff"}
	case "privateresolveWriteoffCommand":
		p.capability = "writeoff.confirm"
		p.operations = []string{"resolveWriteoffCommand"}
	case "privatesuggestID":
		p.capability = "catalog.write"
		p.operations = []string{"suggestID"}
	case "privateAnalyticsstock":
		p.capability = "analytics.read"
		p.operations = []string{"analyticsstock"}
	case "privateAnalyticsmovements":
		p.capability = "analytics.read"
		p.operations = []string{"analyticsmovements"}
	default:
		return p, false
	}
	return p, true
}

func validClaimsShape(segment, kind string) bool {
	data, err := base64.RawURLEncoding.Strict().DecodeString(segment)
	if err != nil {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return false
	}
	required := []string{"iss", "aud", "iat", "nbf", "exp", "jti", "operation", "permissions", "kind", "sub", "org_id", "tenant_id", "lifecycle_version", "azp"}
	switch kind {
	case "user":
		required = append(required, "sid", "session_version", "credential_version", "access_version", "time_zone", "settings_version")
	case "service":
		required = append(required, "runtime_epoch")
	default:
		return false
	}
	if len(fields) != len(required) {
		return false
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return false
		}
	}
	return true
}
