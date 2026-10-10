package main

import (
	"fmt"
	"strconv"
	"time"
)

type serverConfig struct {
	port                    string
	eventsPerSnapshot       int64
	expirationCheckInterval time.Duration
}

func loadConfig(lookup func(string) (string, bool)) (serverConfig, error) {
	cfg := serverConfig{
		port:                    envValue(lookup, "PORT", "8080"),
		eventsPerSnapshot:       100,
		expirationCheckInterval: time.Second,
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
	if value, ok := lookup("EXPIRATION_CHECK_INTERVAL"); ok && value != "" {
		interval, err := time.ParseDuration(value)
		if err != nil {
			return serverConfig{}, fmt.Errorf("EXPIRATION_CHECK_INTERVAL: %w", err)
		}
		if interval <= 0 {
			return serverConfig{}, fmt.Errorf("EXPIRATION_CHECK_INTERVAL must be positive")
		}
		cfg.expirationCheckInterval = interval
	}
	return cfg, nil
}

func envValue(lookup func(string) (string, bool), key, fallback string) string {
	if value, ok := lookup(key); ok && value != "" {
		return value
	}
	return fallback
}
