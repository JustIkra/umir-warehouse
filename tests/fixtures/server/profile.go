package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/adapters/zonedata"
)

const profileAmendment = "umir-owner-profiles/0.1.0-unpublished"
const profileIdentity = "sha256:ee5ea052cf8239a40fdc3aba7c97c0a143260be284c390168a75c8afb7770460"
const profileName = "warehouse-tzdata-missing"
const profilePath = "/owner-profile/v1"
const profileConfigPath = "/run/umir-fixtures/profile.json"

type profileConfig struct {
	Amendment string `json:"amendment"`
	Project   string `json:"project"`
	Profile   string `json:"profile"`
	Contract  string `json:"profileContractIdentity"`
}
type profileRequest struct {
	Amendment          string `json:"amendment"`
	Project            string `json:"project"`
	Profile            string `json:"profile,omitempty"`
	RequestID          string `json:"requestID"`
	ExpectedInstanceID string `json:"expectedInstanceID"`
	ExpectedGeneration string `json:"expectedGeneration"`
	Action             string `json:"action"`
}
type profileClock struct {
	Instant    string `json:"instant"`
	Generation string `json:"generation"`
	InstanceID string `json:"instanceID"`
	Source     string `json:"source"`
	Healthy    bool   `json:"healthy"`
}
type profileBinding struct {
	Project             string `json:"project"`
	Profile             string `json:"profile"`
	Owner               string `json:"owner"`
	InstanceID          string `json:"instanceID"`
	BinarySHA256        string `json:"binarySHA256"`
	ConfigSHA256        string `json:"configSHA256"`
	FixtureSHA256       string `json:"fixtureSHA256"`
	Contract            string `json:"profileContractIdentity"`
	ProfileConfigSHA256 string `json:"profileConfigSHA256"`
}
type timezoneFailure struct {
	SHA256                 string `json:"embeddedArchiveSHA256"`
	Bytes                  int    `json:"embeddedArchiveBytes"`
	Constructor            string `json:"constructor"`
	Error                  string `json:"error"`
	FallbackUsed           bool   `json:"fallbackUsed"`
	BusinessHandlerCreated bool   `json:"businessHandlerCreated"`
}
type refusalReport struct {
	Amendment              string         `json:"amendment"`
	RequestID              string         `json:"requestID"`
	Binding                profileBinding `json:"binding"`
	Clock                  profileClock   `json:"clock"`
	Admission              string         `json:"admission"`
	NormalReady            bool           `json:"normalReady"`
	BusinessHandlerCreated bool           `json:"businessHandlerCreated"`
	Error                  string         `json:"error"`
	Facts                  struct {
		Timezone timezoneFailure `json:"timezone"`
	} `json:"facts"`
}
type catalogFact struct {
	Owner      string `json:"owner"`
	Version    string `json:"version"`
	SHA256     string `json:"archiveSHA256"`
	Bytes      int    `json:"archiveBytes"`
	Toolchain  string `json:"buildToolchain"`
	Ready      bool   `json:"ready"`
	Provenance string `json:"provenanceSHA256"`
}
type catalogReport struct {
	Amendment    string       `json:"amendment"`
	RequestID    string       `json:"requestID"`
	Project      string       `json:"project"`
	InstanceID   string       `json:"instanceID"`
	Clock        profileClock `json:"clock"`
	BinarySHA256 string       `json:"binarySHA256"`
	ConfigSHA256 string       `json:"configSHA256"`
	Catalog      catalogFact  `json:"catalog"`
}

func digestBytes(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func parseProfileConfig(raw []byte, project, selector string) (profileConfig, string, error) {
	var p profileConfig
	if len(raw) > 4096 || decodeObject(raw, &p, "amendment", "project", "profile", "profileContractIdentity") != nil || p.Amendment != profileAmendment || p.Project != project || p.Profile != profileName || selector != p.Profile || p.Contract != profileIdentity {
		return p, "", errors.New("invalid profile config")
	}
	return p, digestBytes(raw), nil
}
func (c *control) profileClockLocked() profileClock {
	return profileClock{c.instant.In(time.FixedZone("Europe/Moscow", 3*60*60)).Format(time.RFC3339Nano), strconv.FormatUint(c.generation, 10), c.instance, "owner", true}
}
func (c *control) profileHTTP(w http.ResponseWriter, r *http.Request) {
	id, err := newUUID()
	if err != nil {
		w.WriteHeader(503)
		return
	}
	reject := func(status int, code string) {
		b, e := json.Marshal(struct {
			Amendment string `json:"amendment"`
			RequestID string `json:"requestID"`
			Error     string `json:"error"`
		}{profileAmendment, id, code})
		if e != nil {
			w.WriteHeader(503)
			return
		}
		writeControl(w, status, b)
	}
	if r.Method != http.MethodPost || r.URL.Path != profilePath || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		reject(400, "InvalidRequest")
		return
	}
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || subtle.ConstantTimeCompare([]byte(auth[0]), []byte("Bearer "+c.credential)) != 1 {
		reject(401, "Unauthorized")
		return
	}
	content, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || content != "application/json" {
		reject(400, "InvalidRequest")
		return
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, 65537))
	if e != nil || len(raw) > 65536 {
		reject(400, "InvalidRequest")
		return
	}
	var req profileRequest
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		reject(400, "InvalidRequest")
		return
	}
	fields := []string{"amendment", "project", "requestID", "expectedInstanceID", "expectedGeneration", "action"}
	var action string
	if json.Unmarshal(obj["action"], &action) != nil {
		reject(400, "InvalidRequest")
		return
	}
	if action == "report" {
		fields = append(fields, "profile")
	}
	if decodeObject(raw, &req, fields...) != nil || req.Amendment != profileAmendment || !projectPattern.MatchString(req.Project) || !uuidPattern.MatchString(req.RequestID) || !uuidPattern.MatchString(req.ExpectedInstanceID) || !generationPattern.MatchString(req.ExpectedGeneration) {
		reject(400, "InvalidRequest")
		return
	}
	id = req.RequestID
	if req.Project != c.project {
		reject(403, "OwnershipMismatch")
		return
	}
	canon, e := canonical(raw)
	if e != nil {
		reject(400, "InvalidRequest")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	select {
	case c.serial <- struct{}{}:
		defer func() { <-c.serial }()
	case <-ctx.Done():
		reject(408, "ProfileExpired")
		return
	}
	if ctx.Err() != nil {
		reject(408, "ProfileExpired")
		return
	}
	if c.profileAdmissionFailed {
		reject(503, "ProfileAdmissionFailed")
		return
	}
	if previous, ok := c.profileCache[id]; ok {
		if previous.canonical != string(canon) {
			reject(409, "RequestConflict")
			return
		}
		writeControl(w, previous.status, previous.body)
		return
	}
	if !c.profileDeadline.IsZero() && !time.Now().Before(c.profileDeadline) {
		reject(408, "ProfileExpired")
		return
	}
	c.clockMu.RLock()
	defer c.clockMu.RUnlock()
	if req.ExpectedInstanceID != c.instance || req.ExpectedGeneration != strconv.FormatUint(c.generation, 10) {
		reject(409, "ClockConflict")
		return
	}
	if req.Profile != "" && (req.Profile != profileName || c.refusal == nil) {
		reject(400, "UnsupportedOwnerProfile")
		return
	}
	if req.Action == "report" && req.Profile == "" {
		reject(400, "UnsupportedOwnerProfile")
		return
	}
	if req.Action != "catalog-report" && req.Action != "report" {
		reject(400, "UnsupportedAction")
		return
	}
	if req.Action == "catalog-report" && (req.Profile != "" || c.refusal != nil) {
		reject(400, "UnsupportedAction")
		return
	}
	if len(c.profileCache) >= 64 {
		reject(413, "ProfileCapacityExceeded")
		return
	}
	var result any
	if req.Action == "report" {
		report := *c.refusal
		report.RequestID = id
		report.Clock = c.profileClockLocked()
		result = report
	} else {
		if c.catalog == nil {
			reject(503, "ProfileAdmissionFailed")
			return
		}
		fact, e := c.catalog(ctx)
		if e != nil {
			reject(503, "ProfileAdmissionFailed")
			return
		}
		result = catalogReport{profileAmendment, id, c.project, c.instance, c.profileClockLocked(), c.binary, c.config, fact}
	}
	body, e := json.Marshal(result)
	if e != nil {
		reject(503, "ProfileAdmissionFailed")
		return
	}
	if c.profileCache == nil {
		c.profileCache = make(map[string]cached)
	}
	c.profileCache[id] = cached{canonical: string(canon), status: 200, body: body}
	writeControl(w, 200, body)
}
func observeCatalog(ctx context.Context, app *process, key, provenance string) (catalogFact, error) {
	info, err := zonedata.Inspect()
	if err != nil || runtime.Version() != "go1.26.9" || !validDigest(provenance) {
		return catalogFact{}, errors.New("catalogue unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://warehouse/internal/warehouse/v2/runtime", nil)
	if err != nil {
		return catalogFact{}, err
	}
	req.Header.Set("X-Umir-Service-Key", key)
	measured := newRuntimeResponse()
	app.ServeHTTP(measured, req)
	var state struct {
		Service, Epoch, TZDataVersion string
		Ready                         bool
	}
	if measured.status != 200 || json.Unmarshal(measured.body.Bytes(), &state) != nil || !state.Ready || state.Service != "warehouse" || state.TZDataVersion != info.Version || state.Epoch != app.registry.Epoch() {
		return catalogFact{}, errors.New("runtime catalogue mismatch")
	}
	return catalogFact{"warehouse", info.Version, info.SHA256, info.Bytes, runtime.Version(), true, provenance}, nil
}

type runtimeResponse struct {
	body   bytes.Buffer
	header http.Header
	status int
}

func newRuntimeResponse() *runtimeResponse {
	return &runtimeResponse{header: make(http.Header), status: 200}
}
func (w *runtimeResponse) Header() http.Header         { return w.header }
func (w *runtimeResponse) WriteHeader(status int)      { w.status = status }
func (w *runtimeResponse) Write(b []byte) (int, error) { return w.body.Write(b) }
