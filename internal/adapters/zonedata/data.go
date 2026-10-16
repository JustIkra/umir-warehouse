package zonedata

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"io"
	"sync"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
)

const Version = "go1.26.9-embedded"

//go:embed zoneinfo.zip
var data []byte

// zones holds every zone loaded so far; the archive is fixed, so a loaded zone
// never changes and the cache is as large as the set of zone names at most.
var zones sync.Map

// Load returns the named zone from the embedded archive. A *time.Location is
// safe for concurrent use, so callers share one value per name.
func Load(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, contract.ErrUnknownTimeZone
	}
	if cached, ok := zones.Load(name); ok {
		if zone, ok := cached.(*time.Location); ok {
			return zone, nil
		}
	}
	zone, err := loadFromArchive(name)
	if err != nil {
		return nil, err
	}
	actual, _ := zones.LoadOrStore(name, zone)
	if shared, ok := actual.(*time.Location); ok {
		return shared, nil
	}
	return zone, nil
}

func loadFromArchive(name string) (*time.Location, error) {
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

// Check reads the archive afresh, not through the cache, so it reports the state of the archive itself.
func Check() error { _, err := loadFromArchive("Europe/Moscow"); return err }
