package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func controlCall(t *testing.T, c *control, op string, data any) *httptest.ResponseRecorder {
	t.Helper()
	id, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	raw := mustJSON(t, request{1, c.project, id, op, mustJSON(t, data)})
	r := httptest.NewRequest("POST", controlPath, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+c.credential)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	return w
}
func TestW6ControlSchemasAndCapacity(t *testing.T) {
	f := testComposition(t)
	c := f.c
	clock := c.readClock()
	for _, op := range []string{"fixture.ready", "clock.read", "clock.checkpoint", "clock.set"} {
		data := map[string]string{}
		if op == "clock.checkpoint" || op == "clock.set" {
			data = map[string]string{"expectedInstanceID": clock.InstanceID, "expectedGeneration": clock.Generation}
		}
		if op == "clock.set" {
			data["instant"] = clock.Instant
		}
		w := controlCall(t, c, op, data)
		if w.Code != 200 {
			t.Fatal(op, w.Code, w.Body)
		}
		t.Logf("WIRE_SCHEMA %s_response %s", strings.ReplaceAll(op, ".", "_"), w.Body.String())
	}
	arm := map[string]any{"faultID": "77777777-7777-4777-8777-777777777777", "point": "before-commit", "operationID": "registerProduct", "commandID": "one", "expireAfterMs": 30000, "count": 1}
	for _, op := range []string{"owner.fault.arm", "owner.fault.read", "owner.fault.clear"} {
		var data any = arm
		if op != "owner.fault.arm" {
			data = map[string]any{"faultID": arm["faultID"]}
		}
		w := controlCall(t, c, op, data)
		if w.Code != 200 {
			t.Fatal(op, w.Code, w.Body)
		}
		t.Logf("WIRE_SCHEMA %s_response %s", strings.ReplaceAll(op, ".", "_"), w.Body.String())
	}
	data := map[string]string{"expectedInstanceID": clock.InstanceID, "expectedGeneration": "2", "instant": "2026-10-13T08:59:59+03:00"}
	w := controlCall(t, c, "clock.set", data)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "ClockBackward") {
		t.Fatal(w.Code, w.Body)
	}
	t.Logf("WIRE_SCHEMA Error %s", w.Body.String())
	c.clockMu.Lock()
	c.generation = maxGeneration
	c.clockMu.Unlock()
	data["expectedGeneration"] = "9999999999999999999"
	data["instant"] = clock.Instant
	if controlCall(t, c, "clock.set", data).Code != 409 {
		t.Fatal("overflow accepted")
	}
	for _, body := range []string{`{"version":1,"version":1}`, `{} {}`, `{"password":"SENSITIVE_SENTINEL"}`, strings.Repeat(" ", 16385)} {
		r := httptest.NewRequest("POST", controlPath, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+c.credential)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code < 400 || strings.Contains(w.Body.String(), "SENSITIVE_SENTINEL") {
			t.Fatal("unsafe malformed response")
		}
	}
	for len(c.cache) < 256 {
		if controlCall(t, c, "clock.read", map[string]string{}).Code != 200 {
			t.Fatal("premature capacity")
		}
	}
	if controlCall(t, c, "clock.read", map[string]string{}).Code != 413 {
		t.Fatal("cache evicted")
	}
}
func TestW6ArmClearUseReviewedMinimalWireShape(t *testing.T) {
	f := testComposition(t)
	arm := map[string]any{"faultID": "77777777-7777-4777-8777-777777777777", "point": "before-commit", "operationID": "registerProduct", "commandID": "one", "expireAfterMs": 30000, "count": 1}
	for _, op := range []string{"owner.fault.arm", "owner.fault.clear"} {
		var data any = arm
		if op == "owner.fault.clear" {
			data = map[string]any{"faultID": arm["faultID"]}
		}
		w := controlCall(t, f.c, op, data)
		var value struct{ Data map[string]json.RawMessage }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &value) != nil {
			t.Fatal(w.Code, w.Body)
		}
		if len(value.Data) != 2 || value.Data["hits"] != nil || value.Data["ownerOutcome"] != nil {
			t.Fatal("arm/clear violates reviewed minimal schema")
		}
	}
}
func TestW6PrivateFilesAndUnixShutdown(t *testing.T) {
	dir, err := os.MkdirTemp("", "w6-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chown(dir, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credential")
	if err = os.WriteFile(path, []byte(strings.Repeat("a", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readPrivate(path, 4096); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = readPrivate(path, 4096); err == nil {
		t.Fatal("unsafe mode")
	}
	link := filepath.Join(dir, "link")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err = readPrivate(link, 4096); err == nil {
		t.Fatal("symlink")
	}
	fifo := filepath.Join(dir, "fifo")
	if err = syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := readPrivate(fifo, 4096); done <- e }()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("FIFO")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked FIFO")
	}
	f := testComposition(t)
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chown(dir, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "control.sock")
	unix, err := privateListener(socket)
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if e := unix.Close(); e != nil {
			t.Error(e)
		}
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- runProcess(ctx, f.app, f.c, tcp, unix) }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	id, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	raw := mustJSON(t, request{1, f.c.project, id, "clock.read", json.RawMessage("{}")})
	r, err := http.NewRequest("POST", "http://fixture"+controlPath, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+f.c.credential)
	res, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err = res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || !strings.Contains(string(b), "owner-injected") {
		t.Fatal("Unix unavailable")
	}
	tcpClient := &http.Client{Timeout: time.Second}
	reply, err := tcpClient.Post("http://"+tcp.Addr().String()+controlPath, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(io.Discard, reply.Body); err != nil {
		t.Fatal(err)
	}
	if err = reply.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if reply.StatusCode == 200 {
		t.Fatal("control exposed on TCP")
	}
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker/listener leak")
	}
	if _, err = os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatal("socket retained", err)
	}
}
