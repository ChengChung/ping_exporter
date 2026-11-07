// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/czerwonk/ping_exporter/config"
	log "github.com/sirupsen/logrus"
)

// getConfigCachePath returns the local cache path for a URL-based config file.
// The cache is stored in a "config-cache" subdirectory using a hash of the URL as the filename.
func getConfigCachePath(configURL string) string {
	// Create a simple filename based on the URL
	// For security, we only use alphanumeric characters and underscores
	filename := hashStringToFilename(configURL)
	cacheDir := filepath.Join(*pushMetricsBaseDir, "config-cache")
	return filepath.Join(cacheDir, filename)
}

// hashStringToFilename converts a string to a safe filename.
// Uses a simple hash-based approach to create a consistent filename.
func hashStringToFilename(s string) string {
	// Use a simple approach: take last part of URL and hash it
	// This ensures consistency and avoids path traversal issues
	hash := 0
	for _, c := range s {
		hash = (hash * 31) + int(c)
	}
	if hash < 0 {
		hash = -hash
	}
	return fmt.Sprintf("config_%d.cache", hash)
}

// saveConfigCache saves the config content to the local cache file.
func saveConfigCache(cachePath string, content []byte) error {
	// Ensure cache directory exists
	cacheDir := filepath.Dir(cachePath)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory %s: %w", cacheDir, err)
	}

	file, err := os.CreateTemp(cacheDir, ".config-*")
	if err != nil {
		return fmt.Errorf("failed to create config cache: %w", err)
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !os.IsNotExist(err) {
			log.WithError(err).Warn("failed to remove temporary config cache")
		}
	}()
	_, writeErr := file.Write(content)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("failed to write config cache: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to close config cache: %w", closeErr)
	}
	if err := os.Rename(file.Name(), cachePath); err != nil {
		return fmt.Errorf("failed to replace config cache: %w", err)
	}

	log.Debugf("config cache saved to %s", cachePath)
	return nil
}

// loadConfigCache loads the config from the local cache file.
func loadConfigCache(cachePath string) ([]byte, error) {
	content, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config cache: %w", err)
	}
	return content, nil
}

// fetchConfigWithCache fetches the config from a URL with fallback to local cache.
// If the URL is not available and a cache exists, it uses the cached version.
// The successful fetch result is saved to cache for future use.
func fetchConfigWithCache(ctx context.Context, configURL string, cachePath string, allowCached bool) (io.ReadCloser, error) {
	content, err := fetchConfigContent(ctx, configURL)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !allowCached {
			return nil, err
		}
		log.WithError(err).Warn("failed to fetch valid HTTP config; trying local cache")
		cached, cacheErr := loadConfigCache(cachePath)
		if cacheErr == nil {
			cacheErr = validateConfigContent(cached)
		}
		if cacheErr != nil {
			return nil, fmt.Errorf("%w; cached config unavailable: %w", err, cacheErr)
		}
		log.Warnf("using cached config from %s", cachePath)
		return io.NopCloser(bytes.NewReader(cached)), nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := saveConfigCache(cachePath, content); err != nil {
		log.WithError(err).Warn("HTTP config loaded but could not be cached")
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func fetchConfigContent(ctx context.Context, configURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, configURL, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot create config request: %w", err)
	}

	resp, err := configHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot fetch config: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.WithError(err).Warn("failed to close config response")
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cannot fetch config: HTTP %s", resp.Status)
	}
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read config response: %w", err)
	}
	if err := validateConfigContent(content); err != nil {
		return nil, err
	}
	return content, nil
}

func validateConfigContent(content []byte) error {
	cfg, err := config.FromYAML(bytes.NewReader(content))
	if err != nil {
		return err
	}
	if cfg.Targets == nil {
		return fmt.Errorf("HTTP config must contain a targets field")
	}
	return nil
}
