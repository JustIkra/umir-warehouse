package main

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestProfileClientFixedPathAndStrictRequest(t *testing.T) {
	raw := `{"amendment":"umir-owner-profiles/0.1.0-unpublished","project":"umir-backend-aaaaaaaaaaaaaaaaaaaaaaaa","requestID":"99999999-9999-4999-8999-999999999999","expectedInstanceID":"11111111-1111-4111-8111-111111111111","expectedGeneration":"1","action":"catalog-report"}`
	if _, err := validProfileInput([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{raw + `{}`, strings.Replace(raw, `"action":`, `"action":"report","action":`, 1), strings.Replace(raw, `catalog-report`, `activate-next`, 1), strings.Replace(raw, `"expectedGeneration":"1"`, `"expectedGeneration":"01"`, 1)} {
		if _, err := validProfileInput([]byte(bad)); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if runProfile(context.Background(), []string{"profile", "--request", "/tmp/foreign.json"}, io.Discard) == nil {
		t.Fatal("arbitrary request file accepted")
	}
}
