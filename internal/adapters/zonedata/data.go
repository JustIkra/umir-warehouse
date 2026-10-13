package zonedata

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"io"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
)

const Version = "go1.26.9-embedded"

//go:embed zoneinfo.zip
var data []byte

func Load(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, contract.ErrUnknownTimeZone
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, contract.ErrTimeZoneDataUnavailable
	}
	for _, file := range archive.File {
		if file.Name != name {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return nil, contract.ErrTimeZoneDataUnavailable
		}
		value, readErr := io.ReadAll(io.LimitReader(reader, 1<<20))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil {
			return nil, contract.ErrTimeZoneDataUnavailable
		}
		zone, err := time.LoadLocationFromTZData(name, value)
		if err != nil {
			return nil, contract.ErrTimeZoneDataUnavailable
		}
		return zone, nil
	}
	return nil, contract.ErrUnknownTimeZone
}
func Check() error { _, err := Load("Europe/Moscow"); return err }
