package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestW6ClockIsInjectedAndReplaysExactly(t *testing.T) {
	c, err := newControl("umir-backend-"+strings.Repeat("a", 24), "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "2026-10-13T09:00:00+03:00", "1", strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64), func(context.Context) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"project":"umir-backend-` + strings.Repeat("a", 24) + `","requestID":"33333333-3333-4333-8333-333333333333","operation":"clock.set","data":{"expectedInstanceID":"11111111-1111-4111-8111-111111111111","expectedGeneration":"1","instant":"2026-10-13T10:00:00+03:00"}}`
	call := func(s string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, controlPath, strings.NewReader(s))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("b", 64))
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w
	}
	first, replay := call(body), call(body)
	if first.Code != 200 || first.Body.String() != replay.Body.String() || c.readClock().Generation != "2" || c.readClock().Owner != "warehouse" {
		t.Fatalf("clock/replay mismatch: %d %s", first.Code, first.Body)
	}
	if !c.now().Equal(time.Date(2026, 10, 13, 7, 0, 0, 0, time.UTC)) {
		t.Fatal("clock not stored")
	}
	if call(strings.Replace(body, "10:00:00", "11:00:00", 1)).Code != 409 {
		t.Fatal("changed replay did not conflict")
	}
}
