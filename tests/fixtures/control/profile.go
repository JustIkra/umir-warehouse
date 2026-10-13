package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"
)

const profileAmendment = "umir-owner-profiles/0.1.0-unpublished"
const profileIdentity = "sha256:ee5ea052cf8239a40fdc3aba7c97c0a143260be284c390168a75c8afb7770460"
const profileRequestPath = "/run/umir-fixtures/profile-request.json"

func validProfileInput(raw []byte) (map[string]json.RawMessage, error) {
	req, err := object(raw)
	if err != nil || len(raw) > 65536 {
		return nil, errors.New("invalid profile request")
	}
	catalog := textField(req, "action") == "catalog-report"
	fields := []string{"amendment", "project", "requestID", "expectedInstanceID", "expectedGeneration", "action"}
	if !catalog {
		fields = append(fields, "profile")
	}
	if !exact(req, fields...) || textField(req, "amendment") != profileAmendment || !projectPattern.MatchString(textField(req, "project")) || !uuidPattern.MatchString(textField(req, "requestID")) || !uuidPattern.MatchString(textField(req, "expectedInstanceID")) || !generationPattern.MatchString(textField(req, "expectedGeneration")) || !catalog && (textField(req, "action") != "report" || textField(req, "profile") != "warehouse-tzdata-missing") {
		return nil, errors.New("invalid profile request")
	}
	return req, nil
}
func validProfileClock(raw []byte, req map[string]json.RawMessage) bool {
	clock, err := object(raw)
	if err != nil || !exact(clock, "instant", "generation", "instanceID", "source", "healthy") {
		return false
	}
	instant, parseErr := time.Parse(time.RFC3339Nano, textField(clock, "instant"))
	if parseErr != nil || instant.Year() < 2000 || instant.Year() > 2099 {
		return false
	}
	return textField(clock, "instanceID") == textField(req, "expectedInstanceID") && textField(clock, "generation") == textField(req, "expectedGeneration") && textField(clock, "source") == "owner" && bytes.Equal(clock["healthy"], []byte("true")) && instantPattern.MatchString(textField(clock, "instant")) && len(textField(clock, "instant")) >= 6 && textField(clock, "instant")[len(textField(clock, "instant"))-6:] == "+03:00"
}
func validateProfileResponse(raw []byte, req map[string]json.RawMessage, status int) bool {
	res, err := object(raw)
	if err != nil || textField(res, "amendment") != profileAmendment || textField(res, "requestID") != textField(req, "requestID") {
		return false
	}
	if status != 200 {
		return exact(res, "amendment", "requestID", "error") && textField(res, "error") != ""
	}
	if !validProfileClock(res["clock"], req) {
		return false
	}
	if textField(req, "action") == "catalog-report" {
		if !exact(res, "amendment", "requestID", "project", "instanceID", "clock", "binarySHA256", "configSHA256", "catalog") || textField(res, "project") != textField(req, "project") || textField(res, "instanceID") != textField(req, "expectedInstanceID") || !digestPattern.MatchString(textField(res, "binarySHA256")) || !digestPattern.MatchString(textField(res, "configSHA256")) {
			return false
		}
		fact, err := object(res["catalog"])
		if err != nil || !exact(fact, "owner", "version", "archiveSHA256", "archiveBytes", "buildToolchain", "ready", "provenanceSHA256") {
			return false
		}
		var size int
		return textField(fact, "owner") == "warehouse" && textField(fact, "version") == "go1.26.9-embedded" && textField(fact, "buildToolchain") == "go1.26.9" && bytes.Equal(fact["ready"], []byte("true")) && digestPattern.MatchString(textField(fact, "archiveSHA256")) && digestPattern.MatchString(textField(fact, "provenanceSHA256")) && json.Unmarshal(fact["archiveBytes"], &size) == nil && size > 0 && size <= 16777216
	}
	if !exact(res, "amendment", "requestID", "binding", "clock", "admission", "normalReady", "businessHandlerCreated", "error", "facts") || textField(res, "admission") != "refused" || !bytes.Equal(res["normalReady"], []byte("false")) || !bytes.Equal(res["businessHandlerCreated"], []byte("false")) || textField(res, "error") != "TimeZoneDataUnavailable" {
		return false
	}
	binding, err := object(res["binding"])
	if err != nil || !exact(binding, "project", "profile", "owner", "instanceID", "binarySHA256", "configSHA256", "fixtureSHA256", "profileContractIdentity", "profileConfigSHA256") {
		return false
	}
	if textField(binding, "project") != textField(req, "project") || textField(binding, "profile") != textField(req, "profile") || textField(binding, "owner") != "warehouse" || textField(binding, "instanceID") != textField(req, "expectedInstanceID") || textField(binding, "profileContractIdentity") != profileIdentity {
		return false
	}
	for _, field := range []string{"binarySHA256", "configSHA256", "fixtureSHA256", "profileConfigSHA256"} {
		if !digestPattern.MatchString(textField(binding, field)) {
			return false
		}
	}
	facts, err := object(res["facts"])
	if err != nil || !exact(facts, "timezone") {
		return false
	}
	zone, err := object(facts["timezone"])
	if err != nil || !exact(zone, "embeddedArchiveSHA256", "embeddedArchiveBytes", "constructor", "error", "fallbackUsed", "businessHandlerCreated") {
		return false
	}
	return textField(zone, "embeddedArchiveSHA256") == "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" && textField(binding, "fixtureSHA256") == textField(zone, "embeddedArchiveSHA256") && bytes.Equal(zone["embeddedArchiveBytes"], []byte("0")) && textField(zone, "constructor") == "bootstrap.NewRegistry" && textField(zone, "error") == "TimeZoneDataUnavailable" && bytes.Equal(zone["fallbackUsed"], []byte("false")) && bytes.Equal(zone["businessHandlerCreated"], []byte("false"))
}
func requestProfile(ctx context.Context, socket, credential string, raw []byte) ([]byte, error) {
	req, err := validProfileInput(raw)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://fixture/owner-profile/v1", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+credential)
	res, err := client.Do(r)
	if err != nil {
		return nil, errors.New("profile unavailable")
	}
	body, readErr := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err = errors.Join(readErr, res.Body.Close()); err != nil || len(body) > 65536 || !validateProfileResponse(body, req, res.StatusCode) {
		return nil, errors.New("invalid profile response")
	}
	if res.StatusCode != 200 {
		return nil, errors.New("profile action refused")
	}
	return body, nil
}
func runProfile(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 3 || args[0] != "profile" || args[1] != "--request" || args[2] != profileRequestPath {
		return errors.New("expected fixed profile request")
	}
	raw, err := readPrivate(profileRequestPath, 65536)
	if err != nil {
		return err
	}
	req, err := validProfileInput(raw)
	if err != nil {
		return err
	}
	config, err := readPrivate(configPath, 16384)
	if err != nil {
		return err
	}
	cfg, err := object(config)
	if err != nil {
		return err
	}
	ref, err := object(cfg["controlCredentialFile"])
	if err != nil || !exact(ref, "target", "path") || textField(ref, "target") != "warehouse" || textField(ref, "path") != credentialPath || textField(cfg, "owner") != "warehouse" || textField(cfg, "protocol") != "umir-operational-fixtures/1.1.0" || textField(cfg, "controlSocket") != socketPath || textField(cfg, "project") != textField(req, "project") {
		return errors.New("profile ownership mismatch")
	}
	info, err := os.Lstat(socketPath)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 || int(stat.Uid) != os.Getuid() || int(stat.Gid) != os.Getgid() {
		return errors.New("socket ownership mismatch")
	}
	credential, err := readPrivate(credentialPath, 4096)
	if err != nil {
		return err
	}
	body, err := requestProfile(ctx, socketPath, string(credential), raw)
	if err != nil {
		return err
	}
	_, err = out.Write(append(body, '\n'))
	return err
}
