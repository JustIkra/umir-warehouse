package integration_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JustIkra/umir-warehouse/bootstrap"
)

func TestW2RuntimeDiscoveryUsesAuthenticatedCurrentEpoch(t *testing.T) {
	f := setup(t)
	for _, key := range []string{"", "bad", "auth-key", "gateway-key", "serving-key"} {
		req := httptest.NewRequest("GET", "/internal/warehouse/v2/runtime", nil)
		if key != "" {
			req.Header.Set("X-Umir-Service-Key", key)
		}
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, req)
		if key == "" || key == "bad" {
			if w.Code != 401 {
				t.Fatalf("anonymous discovery: %d", w.Code)
			}
			continue
		}
		var status struct {
			Service string `json:"service"`
			Epoch   string `json:"epoch"`
			Ready   bool   `json:"ready"`
			Version string `json:"tzdataVersion"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || status.Service != "warehouse" || status.Epoch != epoch || !status.Ready || status.Version != bootstrap.TimeZoneDataVersion {
			t.Fatalf("discovery %+v %d", status, w.Code)
		}
	}
	for _, input := range []struct {
		method, path, header string
		want                 int
	}{
		{"GET", "/internal/warehouse/v2/runtime?x=1", "", 400},
		{"POST", "/internal/warehouse/v2/runtime", "", 400},
		{"GET", "/internal/warehouse/v2/runtime", "Cookie", 403},
		{"GET", "/internal/warehouse/v2/runtime", "X-Umir-Tenant-ID", 403},
		{"GET", "/internal/warehouse/v2/runtime", "Authorization", 403},
	} {
		req := httptest.NewRequest(input.method, input.path, nil)
		req.Header.Set("X-Umir-Service-Key", "auth-key")
		if input.header != "" {
			req.Header.Set(input.header, "untrusted")
		}
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, req)
		if w.Code != input.want || !strings.Contains(w.Body.String(), `"outcome":"unavailable"`) {
			t.Fatalf("status boundary: %d %s", w.Code, w.Body)
		}
	}
}
