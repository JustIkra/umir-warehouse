package zonedata

import (
	"crypto/sha256"
	"encoding/hex"
)

type Info struct {
	Version string
	SHA256  string
	Bytes   int
}

func Inspect() (Info, error) {
	sum := sha256.Sum256(data)
	return Info{Version: Version, SHA256: hex.EncodeToString(sum[:]), Bytes: len(data)}, Check()
}
