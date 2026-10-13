package zonedata

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"github.com/JustIkra/umir-warehouse/contract"
)

func TestInspectMeasuresActualArchiveAndPreservesRefusal(t *testing.T) {
	original := data
	t.Cleanup(func() { data = original })
	for _, archive := range [][]byte{original, nil, []byte("corrupt")} {
		data = archive
		info, err := Inspect()
		if info.Version != Version || info.Bytes != len(archive) || info.SHA256 != fmt.Sprintf("%x", sha256.Sum256(archive)) {
			t.Fatalf("wrong measured metadata: %+v", info)
		}
		if len(archive) == len(original) {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, contract.ErrTimeZoneDataUnavailable) {
			t.Fatalf("lost refusal: %v", err)
		}
	}
}
