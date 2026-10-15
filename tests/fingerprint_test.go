package tests

import (
	"testing"

	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestFingerprintConfirmOrderIdentity(t *testing.T) {
	got := domain.Fingerprint("ConfirmOrder", []byte(`{"proposalID":"p1"}`))
	const want = "67610c5d5f3bb1111e064b8ca1f6c8e020c17f3d3dd9651ebb3a3fc69d9ac3b5"
	if got != want {
		t.Fatalf("Fingerprint() = %q; want %q", got, want)
	}
}
