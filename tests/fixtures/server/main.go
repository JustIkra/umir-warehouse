package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"
)

func binaryDigest() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, readErr := io.Copy(h, f)
	if err := errors.Join(readErr, f.Close()); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func serve(ctx context.Context) error {
	cfg, digest, err := loadConfig()
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
	app, err := compose(cfg, keys, c.now, epoch, nil)
	if err != nil {
		return errors.New("real Warehouse composition rejected config")
	}
	defer app.CloseIdleConnections()
	provenance, err := catalogProvenance(binary)
	if err != nil {
		return err
	}
	c.catalog = func(ctx context.Context) (catalogFact, error) {
		return observeCatalog(ctx, app, keys.Lifecycle, provenance)
	}
	c.faults = app.faults
	c.ready = func(ctx context.Context) bool { return ownerReady(ctx, app, keys.Lifecycle, epoch) }
	unix, err := privateListener(cfg.ControlSocket)
	if err != nil {
		return errors.New("private control listener unavailable")
	}
	tcp, err := net.Listen("tcp", ":8080")
	if err != nil {
		return errors.Join(err, unix.Close())
	}
	return runProcess(ctx, app, c, tcp, unix)
}
func runProcess(ctx context.Context, app *process, c *control, tcp, unix net.Listener) error {
	active, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- app.registry.RunSnapshotWorker(active) }()
	go func() { done <- serveListeners(active, app, c, tcp, unix) }()
	first := <-done
	cancel()
	second := <-done
	if errors.Is(first, context.Canceled) {
		first = nil
	}
	if errors.Is(second, context.Canceled) {
		second = nil
	}
	return errors.Join(first, second)
}
func healthcheck(ctx context.Context) error {
	cfg, digest, err := loadConfig()
	if err != nil {
		return err
	}
	b, err := readPrivate(cfg.ControlCredentialFile.Path, 4096)
	if err != nil {
		return err
	}
	id, err := newUUID()
	if err != nil {
		return err
	}
	body, err := json.Marshal(request{Version: 1, Project: cfg.Project, RequestID: id, Operation: "fixture.ready", Data: json.RawMessage("{}")})
	if err != nil {
		return err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", cfg.ControlSocket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://fixture"+controlPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+string(b))
	r.Header.Set("Content-Type", "application/json")
	res, err := client.Do(r)
	if err != nil {
		return errors.New("fixture unavailable")
	}
	raw, readErr := io.ReadAll(io.LimitReader(res.Body, 262145))
	if err := errors.Join(readErr, res.Body.Close()); err != nil {
		return errors.New("fixture read unavailable")
	}
	var out struct {
		Version   int    `json:"version"`
		Project   string `json:"project"`
		RequestID string `json:"requestID"`
		Operation string `json:"operation"`
		Data      struct {
			Target       string   `json:"target"`
			Ready        bool     `json:"ready"`
			Protocol     string   `json:"protocol"`
			InstanceID   string   `json:"instanceID"`
			BinarySHA256 string   `json:"binarySHA256"`
			ConfigSHA256 string   `json:"configSHA256"`
			Capabilities []string `json:"capabilities"`
		} `json:"data"`
	}
	if res.StatusCode != 200 || len(raw) > 262144 || json.Unmarshal(raw, &out) != nil || out.Version != 1 || out.Project != cfg.Project || out.RequestID != id || out.Operation != "fixture.ready" || out.Data.Target != "warehouse" || out.Data.Protocol != protocolVersion || !out.Data.Ready || !uuidPattern.MatchString(out.Data.InstanceID) || !validDigest(out.Data.BinarySHA256) || out.Data.ConfigSHA256 != digest {
		return errors.New("fixture not ready")
	}
	return nil
}
func run(ctx context.Context, args []string) error {
	if len(args) == 3 && args[0] == "serve-profile" && args[1] == "--profile" {
		return serveProfile(ctx, args[2])
	}
	if len(args) != 1 {
		return errors.New("expected serve or healthcheck")
	}
	switch args[0] {
	case "serve":
		return serve(ctx)
	case "healthcheck":
		return healthcheck(ctx)
	default:
		return errors.New("unsupported command")
	}
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		if _, writeErr := fmt.Fprintln(os.Stderr, "Warehouse fixture failed closed"); writeErr != nil {
			os.Exit(1)
		}
		os.Exit(1)
	}
}
