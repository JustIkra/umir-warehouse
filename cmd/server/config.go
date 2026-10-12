package main

import (
	"fmt"
	"strconv"
	"time"
)

type serverConfig struct {
	port                  string
	internalPort          string
	requestTimeout        time.Duration
	maxRequestBytes       int64
	eventsPerSnapshot     int64
	snapshotRetryInterval time.Duration
}

func loadConfig(lookup func(string) (string, bool)) (serverConfig, error) {
	cfg := serverConfig{
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
