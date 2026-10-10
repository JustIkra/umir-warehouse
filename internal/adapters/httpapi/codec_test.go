package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
)

func TestWireIntegersRejectSignsAndZero(t *testing.T) {
	for _, value := range []string{"-1", "+1", "0", "01", "1e2", "1.0"} {
		if _, err := requiredInt64(&value); err == nil {
			t.Errorf("requiredInt64(%q) unexpectedly accepted", value)
		}
	}
	for _, value := range []string{"1", "9007199254740993", "9223372036854775807"} {
		if _, err := requiredInt64(&value); err != nil {
			t.Errorf("requiredInt64(%q) error = %v", value, err)
		}
	}
}

func TestWireTimestampsRequireExactMoscowRFC3339Syntax(t *testing.T) {
	for _, value := range []string{
		"2026-10-11T12:00:00,1+03:00",
		"2026-10-11T12:00:00.1234567890+03:00",
		"2026-10-11T12:00:00Z",
		"2026-10-11T12:00:00+02:00",
		"2026-10-11T12:00+03:00",
		"2026-10-11T12:00:60+03:00",
	} {
		if _, err := requiredTime(&value); err == nil {
			t.Errorf("requiredTime(%q) unexpectedly accepted", value)
		}
	}
	value := "2026-10-11T12:00:00.123456789+03:00"
	if _, err := requiredTime(&value); err != nil {
		t.Fatalf("requiredTime(valid timestamp) error = %v", err)
	}
}

func TestWireTimestampPreservesFractionalSeconds(t *testing.T) {
	value := time.Date(2026, time.October, 11, 12, 0, 0, 123456789, time.FixedZone("MSK", 3*60*60))
	if got := wireTime(value); got != "2026-10-11T12:00:00.123456789+03:00" {
		t.Fatalf("wireTime() = %q, want fractional seconds preserved", got)
	}
}

func TestUnknownOutcomeTakesPrecedenceOverJoinedDefiniteSentinel(t *testing.T) {
	err := errors.Join(contract.ErrUnknownWriteOutcome, contract.ErrInvalidArgument)
	status, code, _, outcome := classifyError(err, true)
	if status != http.StatusInternalServerError || code != "InternalError" || outcome != "unknown" {
		t.Fatalf("classifyError() = (%d, %q, %q), want unknown technical outcome", status, code, outcome)
	}
}

func TestReadFailuresUseUnavailableOutcome(t *testing.T) {
	status, code, _, outcome := classifyError(contract.ErrInvalidArgument, false)
	if status != http.StatusBadRequest || code != "InvalidArgument" || outcome != "unavailable" {
		t.Fatalf("read error mapping = (%d, %q, %q), want InvalidArgument/unavailable", status, code, outcome)
	}
	status, code, _, outcome = classifyError(contract.ErrDishNotFound, false)
	if status != http.StatusNotFound || code != "DishNotFound" || outcome != "unavailable" {
		t.Fatalf("dish read error mapping = (%d, %q, %q), want DishNotFound/unavailable", status, code, outcome)
	}
}

func TestStrictJSONRejectsCaseAliasesAndInvalidUTF8(t *testing.T) {
	for _, body := range []string{
		`{"CommandID":"c1","productID":"p1","name":"Milk","baseUnit":"ml"}`,
		`{"commandID":"c1","CommandID":"c2","productID":"p1","name":"Milk","baseUnit":"ml"}`,
	} {
		request := httptest.NewRequest("POST", "/", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		var target productRequest
		if err := decodeRequest(request, 1024, &target); err == nil {
			t.Errorf("decodeRequest accepted case alias body %s", body)
		}
	}
	request := httptest.NewRequest("POST", "/", strings.NewReader(string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'})))
	request.Header.Set("Content-Type", "application/json")
	var target productRequest
	if err := decodeRequest(request, 1024, &target); err == nil {
		t.Fatal("decodeRequest accepted invalid UTF-8")
	}
}
