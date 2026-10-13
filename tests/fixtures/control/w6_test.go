package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestW6HelperAcceptsImplementedFaultCapabilities(t *testing.T) {
	req := map[string]json.RawMessage{"version": json.RawMessage("1"), "project": json.RawMessage(`"umir-backend-aaaaaaaaaaaaaaaaaaaaaaaa"`), "requestID": json.RawMessage(`"11111111-1111-4111-8111-111111111111"`), "operation": json.RawMessage(`"fixture.ready"`)}
	raw := []byte(`{"version":1,"project":"umir-backend-aaaaaaaaaaaaaaaaaaaaaaaa","requestID":"11111111-1111-4111-8111-111111111111","operation":"fixture.ready","data":{"target":"warehouse","instanceID":"22222222-2222-4222-8222-222222222222","protocol":"umir-operational-fixtures/1.1.0","binarySHA256":"` + strings.Repeat("a", 64) + `","configSHA256":"` + strings.Repeat("b", 64) + `","ready":true,"capabilities":["fixture.ready","clock.read","clock.set","clock.checkpoint","owner.fault.arm","owner.fault.read","owner.fault.clear"]}}`)
	if !validateResponse(raw, req, 200) {
		t.Fatal("helper rejects actual implemented Warehouse capabilities")
	}
}
