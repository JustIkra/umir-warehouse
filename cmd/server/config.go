package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type serverConfig struct {
	apiMode               string
	authJWKSURL           string
	authServiceKey        string
	callerKeys            map[string]string
	allowPlainHTTP        bool
	port                  string
	internalPort          string
	requestTimeout        time.Duration
	maxRequestBytes       int64
	eventsPerSnapshot     int64
	snapshotRetryInterval time.Duration
}

// loadConfig reads the configuration from the environment; readFile reads the
// files named by the <NAME>_FILE variables of the service keys.
func loadConfig(lookup func(string) (string, bool), readFile func(string) ([]byte, error)) (serverConfig, error) {
	if value, ok := lookup("WAREHOUSE_API_MODE"); !ok || value == "" {
		return serverConfig{}, fmt.Errorf("WAREHOUSE_API_MODE is required: set it to legacy or v2")
	}
	cfg := serverConfig{
		apiMode:               envValue(lookup, "WAREHOUSE_API_MODE", ""),
		port:                  envValue(lookup, "PORT", "8080"),
		internalPort:          envValue(lookup, "INTERNAL_PORT", "8082"),
		requestTimeout:        8 * time.Second,
		maxRequestBytes:       1 << 20,
		eventsPerSnapshot:     100,
		snapshotRetryInterval: time.Second,
	}
	if value, ok := lookup("EVENTS_PER_SNAPSHOT"); ok && value != "" {
		threshold, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return serverConfig{}, fmt.Errorf("EVENTS_PER_SNAPSHOT: %w", err)
		}
		if threshold <= 0 {
			return serverConfig{}, fmt.Errorf("EVENTS_PER_SNAPSHOT must be positive")
		}
		cfg.eventsPerSnapshot = threshold
	}
	if value, ok := lookup("SNAPSHOT_RETRY_INTERVAL"); ok && value != "" {
		interval, err := time.ParseDuration(value)
		if err != nil {
			return serverConfig{}, fmt.Errorf("SNAPSHOT_RETRY_INTERVAL: %w", err)
		}
		if interval <= 0 {
			return serverConfig{}, fmt.Errorf("SNAPSHOT_RETRY_INTERVAL must be positive")
		}
		cfg.snapshotRetryInterval = interval
	}
	if value, ok := lookup("REQUEST_TIMEOUT"); ok && value != "" {
		timeout, err := time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return serverConfig{}, fmt.Errorf("REQUEST_TIMEOUT must be positive")
		}
		cfg.requestTimeout = timeout
	}
	if value, ok := lookup("MAX_REQUEST_BYTES"); ok && value != "" {
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil || limit <= 0 || limit > 64<<20 {
			return serverConfig{}, fmt.Errorf("MAX_REQUEST_BYTES must be between 1 and 67108864")
		}
		cfg.maxRequestBytes = limit
	}
	publicPort, err := validatePort(cfg.port)
	if err != nil {
		return serverConfig{}, fmt.Errorf("PORT: %w", err)
	}
	internalPort, err := validatePort(cfg.internalPort)
	if err != nil {
		return serverConfig{}, fmt.Errorf("INTERNAL_PORT: %w", err)
	}
	if publicPort == internalPort {
		return serverConfig{}, fmt.Errorf("INTERNAL_PORT must differ from PORT")
	}
	if cfg.apiMode != "legacy" && cfg.apiMode != "v2" {
		return serverConfig{}, fmt.Errorf("WAREHOUSE_API_MODE must be legacy or v2")
	}
	if cfg.apiMode == "v2" {
		cfg.authJWKSURL = envValue(lookup, "AUTH_JWKS_URL", "")
		keys := make(map[string]string, 4)
		for _, name := range []string{"AUTH_SERVICE_KEY", "AUTH_RUNTIME_SERVICE_KEY", "GATEWAY_SERVICE_KEY", "SERVING_SERVICE_KEY"} {
			key, err := serviceKey(lookup, readFile, name)
			if err != nil {
				return serverConfig{}, err
			}
			keys[name] = key
		}
		cfg.authServiceKey = keys["AUTH_SERVICE_KEY"]
		cfg.allowPlainHTTP = envValue(lookup, "AUTH_ALLOW_PLAIN_HTTP", "false") == "true"
		if cfg.maxRequestBytes > 1<<20 {
			return serverConfig{}, fmt.Errorf("v2 MAX_REQUEST_BYTES must be at most 1048576")
		}
		cfg.callerKeys = map[string]string{"umir-auth": keys["AUTH_RUNTIME_SERVICE_KEY"], "umir-gateway": keys["GATEWAY_SERVICE_KEY"], "umir-serving": keys["SERVING_SERVICE_KEY"]}
		seen := make(map[string]bool)
		for _, key := range cfg.callerKeys {
			if key == "" || seen[key] {
				return serverConfig{}, fmt.Errorf("v2 requires distinct caller transport keys")
			}
			seen[key] = true
		}
		address, err := url.Parse(cfg.authJWKSURL)
		if err != nil || address.Host == "" || address.User != nil || address.Path != "/internal/auth/v1/jwks" || address.RawQuery != "" || address.Fragment != "" || (address.Scheme != "https" && (!cfg.allowPlainHTTP || address.Scheme != "http")) || cfg.authServiceKey == "" {
			return serverConfig{}, fmt.Errorf("v2 requires authenticated Auth JWKS with HTTPS or explicit isolated-network HTTP")
		}
	}
	return cfg, nil
}

func validatePort(value string) (int, error) {
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 || number > 65535 {
		return 0, fmt.Errorf("port must be an integer from 1 through 65535")
	}
	return number, nil
}

func envValue(lookup func(string) (string, bool), key, fallback string) string {
	if value, ok := lookup(key); ok && value != "" {
		return value
	}
	return fallback
}

// serviceKey returns a service key from NAME or from the file named by
// NAME_FILE, without its trailing newline. Setting both is an error. Errors
// name the variable and never carry the key.
func serviceKey(lookup func(string) (string, bool), readFile func(string) ([]byte, error), name string) (string, error) {
	value := envValue(lookup, name, "")
	path := envValue(lookup, name+"_FILE", "")
	switch {
	case value != "" && path != "":
		return "", fmt.Errorf("%s and %s_FILE are both set; use one", name, name)
	case path == "":
		return value, nil
	}
	content, err := readFile(path)
	if err != nil {
		return "", fmt.Errorf("%s_FILE: the file cannot be read", name)
	}
	key := strings.TrimSuffix(strings.TrimSuffix(string(content), "\n"), "\r")
	if key == "" {
		return "", fmt.Errorf("%s_FILE: the file is empty", name)
	}
	return key, nil
}
