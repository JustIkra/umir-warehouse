package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

const secretValue = "s3cr3t-value-not-for-logs"

func v2Env(extra map[string]string) func(string) (string, bool) {
	values := map[string]string{
		"WAREHOUSE_API_MODE": "v2", "AUTH_JWKS_URL": "http://auth/internal/auth/v1/jwks", "AUTH_ALLOW_PLAIN_HTTP": "true",
		"AUTH_SERVICE_KEY": "own", "AUTH_RUNTIME_SERVICE_KEY": "runtime", "GATEWAY_SERVICE_KEY": "gateway", "SERVING_SERVICE_KEY": "serving",
	}
	for key, value := range extra {
		if value == "" {
			delete(values, key)
			continue
		}
		values[key] = value
	}
	return func(key string) (string, bool) { v, ok := values[key]; return v, ok }
}

func filesOf(files map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		content, ok := files[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return []byte(content), nil
	}
}

func TestServiceKeyFromFile(t *testing.T) {
	cfg, err := loadConfig(v2Env(map[string]string{
		"AUTH_SERVICE_KEY": "", "AUTH_SERVICE_KEY_FILE": "/run/secrets/auth",
		"AUTH_RUNTIME_SERVICE_KEY": "", "AUTH_RUNTIME_SERVICE_KEY_FILE": "/run/secrets/runtime",
		"GATEWAY_SERVICE_KEY": "", "GATEWAY_SERVICE_KEY_FILE": "/run/secrets/gateway",
		"SERVING_SERVICE_KEY": "", "SERVING_SERVICE_KEY_FILE": "/run/secrets/serving",
	}), filesOf(map[string]string{
		"/run/secrets/auth": "auth-key\n", "/run/secrets/runtime": "runtime-key\r\n",
		"/run/secrets/gateway": "gateway-key", "/run/secrets/serving": "serving-key\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.authServiceKey != "auth-key" || cfg.callerKeys["umir-auth"] != "runtime-key" || cfg.callerKeys["umir-gateway"] != "gateway-key" || cfg.callerKeys["umir-serving"] != "serving-key" {
		t.Fatalf("keys = %q %v; want values without the trailing newline", cfg.authServiceKey, cfg.callerKeys)
	}
}

func TestServiceKeyBothSetIsError(t *testing.T) {
	for _, name := range []string{"AUTH_SERVICE_KEY", "AUTH_RUNTIME_SERVICE_KEY", "GATEWAY_SERVICE_KEY", "SERVING_SERVICE_KEY"} {
		_, err := loadConfig(v2Env(map[string]string{name + "_FILE": "/run/secrets/key"}), filesOf(map[string]string{"/run/secrets/key": "other\n"}))
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("both %s and %s_FILE set: error = %v; want one naming %s", name, name, err, name)
		}
	}
}

func TestServiceKeyNotInErrors(t *testing.T) {
	cases := map[string]struct {
		extra map[string]string
		files map[string]string
		read  func(string) ([]byte, error)
	}{
		"both set":                 {extra: map[string]string{"GATEWAY_SERVICE_KEY": secretValue, "GATEWAY_SERVICE_KEY_FILE": "/key"}, files: map[string]string{"/key": secretValue}},
		"empty file":               {extra: map[string]string{"GATEWAY_SERVICE_KEY": "", "GATEWAY_SERVICE_KEY_FILE": "/key"}, files: map[string]string{"/key": "\n"}},
		"unreadable":               {extra: map[string]string{"GATEWAY_SERVICE_KEY": "", "GATEWAY_SERVICE_KEY_FILE": "/key"}, read: func(string) ([]byte, error) { return nil, errors.New("read failed: " + secretValue) }},
		"duplicate of another key": {extra: map[string]string{"GATEWAY_SERVICE_KEY": "", "GATEWAY_SERVICE_KEY_FILE": "/key", "SERVING_SERVICE_KEY": secretValue}, files: map[string]string{"/key": secretValue}},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			read := test.read
			if read == nil {
				read = filesOf(test.files)
			}
			_, err := loadConfig(v2Env(test.extra), read)
			if err == nil {
				t.Fatal("loadConfig() accepted the configuration")
			}
			if strings.Contains(err.Error(), secretValue) {
				t.Fatalf("the error contains the key: %v", err)
			}
		})
	}
}

func TestLoadConfigRequiresExplicitAPIMode(t *testing.T) {
	for _, value := range []string{"", "other"} {
		_, err := loadConfig(func(key string) (string, bool) {
			if key == "WAREHOUSE_API_MODE" && value != "" {
				return value, true
			}
			return "", false
		}, os.ReadFile)
		if err == nil || !strings.Contains(err.Error(), "WAREHOUSE_API_MODE") {
			t.Fatalf("mode %q: error = %v; want one naming WAREHOUSE_API_MODE", value, err)
		}
	}
	cfg, err := loadConfig(func(key string) (string, bool) {
		if key == "WAREHOUSE_API_MODE" {
			return "legacy", true
		}
		return "", false
	}, os.ReadFile)
	if err != nil || cfg.apiMode != "legacy" {
		t.Fatalf("explicit legacy = %+v, %v", cfg, err)
	}
}
