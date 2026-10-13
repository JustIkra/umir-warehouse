package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/zonedata"
	"github.com/JustIkra/umir-warehouse/tests/fixtures/profiles"
)

const provenancePath = "/usr/local/lib/umir/warehouse-provenance.json"

func catalogProvenance(binary string) (string, error) {
	info, err := os.Lstat(provenancePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 4096 {
		return "", errors.New("invalid provenance")
	}
	raw, err := os.ReadFile(provenancePath)
	if err != nil {
		return "", err
	}
	var p profiles.CatalogProvenance
	if decodeObject(raw, &p, "owner", "version", "archiveSHA256", "archiveBytes", "embeddedArchiveSHA256", "buildToolchain", "toolchainImageID", "toolchainSourceSHA256", "binarySHA256", "sourceKind") != nil {
		return "", errors.New("invalid provenance")
	}
	measured, err := zonedata.Inspect()
	if err != nil {
		return "", err
	}
	if p.Owner != "warehouse" || p.Version != measured.Version || p.ArchiveSHA256 != measured.SHA256 || p.EmbeddedArchiveSHA256 != measured.SHA256 || p.ArchiveBytes != measured.Bytes || p.BuildToolchain != runtime.Version() || p.BuildToolchain != "go1.26.9" || p.BinarySHA256 != binary || p.SourceKind != "warehouse-embedded" || !validDigest(p.ToolchainSourceSHA256) || len(p.ToolchainImageID) != 71 || p.ToolchainImageID[:7] != "sha256:" || !validDigest(p.ToolchainImageID[7:]) {
		return "", errors.New("provenance mismatch")
	}
	return digestBytes(raw), nil
}
func serveProfile(ctx context.Context, selector string) error {
	cfg, digest, err := loadConfig()
	if err != nil {
		return err
	}
	raw, err := readPrivate(profileConfigPath, 4096)
	if err != nil {
		return err
	}
	_, profileDigest, err := parseProfileConfig(raw, cfg.Project, selector)
	if err != nil {
		return err
	}
	keys, credential, err := readKeys(cfg)
	if err != nil {
		return err
	}
	instance, err := newUUID()
	if err != nil {
		return err
	}
	epoch, err := newUUID()
	if err != nil {
		return err
	}
	binary, err := binaryDigest()
	if err != nil {
		return err
	}
	c, err := newControl(cfg.Project, instance, cfg.FixtureID, cfg.InitialInstant, cfg.ClockGeneration, credential, binary, digest, func(context.Context) bool { return false })
	if err != nil {
		return err
	}
	c.profileOnly = true
	c.profileDeadline = time.Now().Add(300 * time.Second)
	unix, err := privateListener(cfg.ControlSocket)
	if err != nil {
		return err
	}
	active, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	server := &http.Server{Handler: c, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 16384, BaseContext: func(net.Listener) context.Context { return active }}
	done := make(chan error, 1)
	c.serial <- struct{}{}
	go func() { done <- server.Serve(unix) }()
	admitted, stop := context.WithTimeout(active, 10*time.Second)
	app, compositionErr := compose(cfg, keys, c.now, epoch, nil)
	info, inspectErr := zonedata.Inspect()
	if app != nil {
		app.CloseIdleConnections()
	}
	if admitted.Err() != nil || app != nil || !errors.Is(compositionErr, contract.ErrTimeZoneDataUnavailable) || !errors.Is(inspectErr, contract.ErrTimeZoneDataUnavailable) || info.Bytes != 0 {
		c.profileAdmissionFailed = true
	} else {
		report := &refusalReport{Amendment: profileAmendment, Binding: profileBinding{cfg.Project, selector, "warehouse", instance, binary, digest, info.SHA256, profileIdentity, profileDigest}, Admission: "refused", Error: "TimeZoneDataUnavailable"}
		report.Facts.Timezone = timezoneFailure{info.SHA256, info.Bytes, "bootstrap.NewRegistry", "TimeZoneDataUnavailable", false, false}
		c.refusal = report
	}
	stop()
	<-c.serial
	var result error
	received := false
	select {
	case <-active.Done():
	case result = <-done:
		received = true
	}
	if errors.Is(result, http.ErrServerClosed) {
		result = nil
	}
	cancel()
	shutdown, finish := context.WithTimeout(context.Background(), 5*time.Second)
	defer finish()
	if err := server.Shutdown(shutdown); err != nil {
		result = errors.Join(result, err, server.Close())
	}
	if !received {
		serveErr := <-done
		if !errors.Is(serveErr, http.ErrServerClosed) {
			result = errors.Join(result, serveErr)
		}
	}
	return result
}
