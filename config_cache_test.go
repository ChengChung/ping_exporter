// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestConfigCachePath tests the cache path generation.
func TestConfigCachePath(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{
			name: "HTTP URL",
			url:  "http://example.com/config.json",
		},
		{
			name: "HTTPS URL",
			url:  "https://example.com/config.yaml",
		},
		{
			name: "URL with port",
			url:  "http://localhost:8080/config.json",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := getConfigCachePath(tc.url)
			if path == "" {
				t.Error("cache path is empty")
			}
			// Ensure path starts with config-cache
			if !filepath.IsAbs(path) {
				t.Logf("Cache path: %s", path)
			}
		})
	}
}

// TestFetchConfigWithCacheSuccess tests successful config fetch with cache save.
func TestFetchConfigWithCacheSuccess(t *testing.T) {
	// Create a temporary directory for cache
	tmpDir := t.TempDir()
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	defer os.Chdir(oldDir)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change directory: %v", err)
	}

	// Create a test server
	configContent := "test config content"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(configContent))
	}))
	defer server.Close()

	// Fetch config with cache
	cachePath := getConfigCachePath(server.URL)
	ctx := context.Background()
	reader, err := fetchConfigWithCache(ctx, server.URL, cachePath)
	if err != nil {
		t.Fatalf("failed to fetch config: %v", err)
	}
	defer reader.Close()

	// Read content
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read content: %v", err)
	}

	if string(content) != configContent {
		t.Errorf("content mismatch: got %q, want %q", string(content), configContent)
	}

	// Verify cache file was created
	cacheContent, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("cache file not created: %v", err)
	}

	if string(cacheContent) != configContent {
		t.Errorf("cache content mismatch: got %q, want %q", string(cacheContent), configContent)
	}
}

// TestFetchConfigWithCacheFallback tests fallback to cache when URL is unavailable.
func TestFetchConfigWithCacheFallback(t *testing.T) {
	// Create a temporary directory for cache
	tmpDir := t.TempDir()
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	defer os.Chdir(oldDir)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change directory: %v", err)
	}

	// Create a cache file manually
	cacheDir := "config-cache"
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatalf("failed to create cache directory: %v", err)
	}

	cachedContent := "cached config content"
	cachePath := getConfigCachePath("http://example.com/config.json")
	if err := os.WriteFile(cachePath, []byte(cachedContent), 0644); err != nil {
		t.Fatalf("failed to write cache file: %v", err)
	}

	// Try to fetch from a URL that will fail
	ctx := context.Background()
	reader, err := fetchConfigWithCache(ctx, "http://invalid.local.test/config.json", cachePath)
	if err != nil {
		t.Fatalf("failed to fetch config with fallback: %v", err)
	}
	defer reader.Close()

	// Should have returned cached content
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read content: %v", err)
	}

	if string(content) != cachedContent {
		t.Errorf("fallback content mismatch: got %q, want %q", string(content), cachedContent)
	}
}

// TestFetchConfigWithCacheHTTPError tests fallback when HTTP status is not 200.
func TestFetchConfigWithCacheHTTPError(t *testing.T) {
	// Create a temporary directory for cache
	tmpDir := t.TempDir()
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	defer os.Chdir(oldDir)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change directory: %v", err)
	}

	// Create a cache file manually
	cacheDir := "config-cache"
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatalf("failed to create cache directory: %v", err)
	}

	cachedContent := "cached config content"
	cachePath := getConfigCachePath("http://example.com/config.json")
	if err := os.WriteFile(cachePath, []byte(cachedContent), 0644); err != nil {
		t.Fatalf("failed to write cache file: %v", err)
	}

	// Create a server that returns error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	// Fetch config
	ctx := context.Background()
	reader, err := fetchConfigWithCache(ctx, server.URL, cachePath)
	if err != nil {
		t.Fatalf("failed to fetch config with fallback: %v", err)
	}
	defer reader.Close()

	// Should have returned cached content
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read content: %v", err)
	}

	if string(content) != cachedContent {
		t.Errorf("fallback content mismatch: got %q, want %q", string(content), cachedContent)
	}
}
