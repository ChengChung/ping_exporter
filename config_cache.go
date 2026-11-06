// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

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

	// Write cache file with restricted permissions
	if err := os.WriteFile(cachePath, content, 0644); err != nil {
		return fmt.Errorf("failed to write config cache: %w", err)
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
	log.Warnf("config cache loaded from %s (using fallback due to remote unavailable)", cachePath)
	return content, nil
}

// fetchConfigWithCache fetches the config from a URL with fallback to local cache.
// If the URL is not available and a cache exists, it uses the cached version.
// The successful fetch result is saved to cache for future use.
func fetchConfigWithCache(ctx context.Context, configURL string, cachePath string) (io.ReadCloser, error) {
	// Try to fetch from URL first
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, configURL, nil)
	if err != nil {
		// URL parsing failed, try cache
		log.Warnf("failed to create config request: %v", err)
		content, cacheErr := loadConfigCache(cachePath)
		if cacheErr == nil {
			return io.NopCloser(newBytesReader(content)), nil
		}
		// Return original error if cache also failed
		return nil, fmt.Errorf("cannot create config request: %w (and no cache available)", err)
	}

	resp, err := configHTTPClient.Do(req)
	if err != nil {
		// Network error, try cache
		log.Warnf("failed to fetch config from %s: %v, trying cache...", configURL, err)
		content, cacheErr := loadConfigCache(cachePath)
		if cacheErr == nil {
			return io.NopCloser(newBytesReader(content)), nil
		}
		// Return original error if cache also failed
		return nil, fmt.Errorf("cannot fetch config: %w (and no cache available)", err)
	}

	// Check HTTP status
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		// HTTP error, try cache
		log.Warnf("config fetch returned HTTP %s, trying cache...", resp.Status)
		content, cacheErr := loadConfigCache(cachePath)
		if cacheErr == nil {
			return io.NopCloser(newBytesReader(content)), nil
		}
		// Return original error if cache also failed
		return nil, fmt.Errorf("cannot fetch config: HTTP %s (and no cache available)", resp.Status)
	}

	// Successfully fetched, read and cache the content
	content, err := io.ReadAll(resp.Body)
	resp.Body.Close()

	if err != nil {
		log.Warnf("failed to read config response body: %v", err)
		// Try cache as fallback
		cacheContent, cacheErr := loadConfigCache(cachePath)
		if cacheErr == nil {
			return io.NopCloser(newBytesReader(cacheContent)), nil
		}
		return nil, fmt.Errorf("failed to read config response: %w", err)
	}

	// Save to cache for future use
	if err := saveConfigCache(cachePath, content); err != nil {
		log.Warnf("failed to save config cache: %v", err)
		// Continue anyway, cache saving is not critical
	}

	log.Debugf("config successfully fetched from %s and cached", configURL)
	return io.NopCloser(newBytesReader(content)), nil
}

// bytesReader implements io.Reader for byte slices.
type bytesReader struct {
	data []byte
	pos  int
}

func newBytesReader(data []byte) *bytesReader {
	return &bytesReader{data: data, pos: 0}
}

func (br *bytesReader) Read(p []byte) (int, error) {
	if br.pos >= len(br.data) {
		return 0, io.EOF
	}
	n := copy(p, br.data[br.pos:])
	br.pos += n
	return n, nil
}
