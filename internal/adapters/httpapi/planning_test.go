package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JustIkra/umir-warehouse/contract"
)

func TestFINPlanningOversizedReplyIsTypedFailure(t *testing.T) {
	w := httptest.NewRecorder()
	writePlanningResult(w, contract.ReportIdentity{}, strings.Repeat("x", 16<<20))
	if w.Code != 503 || !strings.Contains(w.Body.String(), `"code":"LimitExceeded"`) || strings.Contains(w.Body.String(), strings.Repeat("x", 100)) {
		t.Fatalf("oversized reply became partial success: %d %s", w.Code, w.Body)
	}
}
