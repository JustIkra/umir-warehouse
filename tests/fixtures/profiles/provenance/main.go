package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/JustIkra/umir-warehouse/internal/adapters/zonedata"
	"github.com/JustIkra/umir-warehouse/tests/fixtures/profiles"
)

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	if err = errors.Join(err, f.Close()); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func generate() error {
	if runtime.Version() != "go1.26.9" {
		return errors.New("unexpected toolchain")
	}
	image := os.Getenv("TOOLCHAIN_IMAGE_ID")
	if !strings.HasPrefix(image, "sha256:") || len(image) != 71 {
		return errors.New("missing exact toolchain identity")
	}
	if b, err := hex.DecodeString(strings.TrimPrefix(image, "sha256:")); err != nil || len(b) != 32 {
		return errors.New("invalid toolchain identity")
	}
	binary, err := fileDigest("/out/fixture-server")
	if err != nil {
		return err
	}
	source, err := fileDigest("/usr/local/go/src/time/tzdata/zzipdata.go")
	if err != nil {
		return err
	}
	info, err := zonedata.Inspect()
	if err != nil && info.Bytes != 0 {
		return err
	}
	raw, err := json.Marshal(profiles.CatalogProvenance{Owner: "warehouse", Version: info.Version, ArchiveSHA256: info.SHA256, ArchiveBytes: info.Bytes, EmbeddedArchiveSHA256: info.SHA256, BuildToolchain: runtime.Version(), ToolchainImageID: image, ToolchainSourceSHA256: source, BinarySHA256: binary, SourceKind: "warehouse-embedded"})
	if err != nil {
		return err
	}
	return os.WriteFile("/out/warehouse-provenance.json", raw, 0644)
}
func main() {
	if generate() != nil {
		os.Exit(1)
	}
}
