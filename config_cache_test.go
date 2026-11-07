// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigCachePath(t *testing.T) {
	preserveConfigFlags(t)
	first := getConfigCachePath("https://example.test/config?node=1")
	second := getConfigCachePath("https://example.test/config?node=2")
	if filepath.Dir(first) != filepath.Join(*pushMetricsBaseDir, "config-cache") || first == second {
		t.Fatalf("unexpected cache paths: %s, %s", first, second)
	}
}

func TestLoadHTTPConfigCachesAndRecovers(t *testing.T) {
	preserveConfigFlags(t)
	body := "targets: [127.0.0.1]\nlabels: {site: initial}\n"
	status := http.StatusOK
	truncated := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if truncated {
			w.Header().Set("Content-Length", "1000")
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	*configFile = server.URL
	configHTTPClient = server.Client()

	assertLoad := func(want string) {
		t.Helper()
		cfg, err := loadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Targets) != 1 || cfg.Labels["site"] != want {
			t.Fatalf("unexpected loaded config: %#v", cfg)
		}
	}
	assertLoad("initial")
	cachePath := getConfigCachePath(server.URL)
	saved, err := os.ReadFile(cachePath)
	if err != nil || string(saved) != body {
		t.Fatalf("cache = %q, err = %v", saved, err)
	}

	body = "targets: [127.0.0.2]\nlabels: {site: updated}\n"
	assertLoad("updated")
	lastGood := body
	for _, invalid := range []string{"targets: [", "", "{}", "targets: null"} {
		body = invalid
		assertLoad("updated")
		saved, err := os.ReadFile(cachePath)
		if err != nil || string(saved) != lastGood {
			t.Fatalf("invalid response replaced cache: %q, %v", saved, err)
		}
	}
	status = http.StatusServiceUnavailable
	assertLoad("updated")
	status = http.StatusOK
	truncated = true
	assertLoad("updated")
	server.Close()
	assertLoad("updated")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := loadConfigContext(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled load used cache: %v", err)
	}
	if err := os.WriteFile(cachePath, []byte("targets: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "cached config unavailable") {
		t.Fatalf("corrupt cache accepted: %v", err)
	}
}

func TestHTTPConfigCacheExplicitEmptyTargets(t *testing.T) {
	preserveConfigFlags(t)
	*targetFlag = []string{"127.0.0.1"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "targets: []")
	}))
	defer server.Close()
	*configFile = server.URL
	for i := 0; i < 2; i++ {
		cfg, err := loadConfig()
		if err != nil || cfg.Targets == nil || len(cfg.Targets) != 0 {
			t.Fatalf("empty target config not preserved: %#v, %v", cfg, err)
		}
		server.Close()
	}
}

func TestHTTPConfigCacheWriteFailure(t *testing.T) {
	preserveConfigFlags(t)
	// A regular file in place of the cache directory fails even when run as root.
	if err := os.WriteFile(filepath.Join(*pushMetricsBaseDir, "config-cache"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "targets: [127.0.0.1]")
	}))
	defer server.Close()
	*configFile = server.URL
	if _, err := loadConfig(); err != nil {
		t.Fatalf("cache write failure should not discard a valid remote config: %v", err)
	}
}

func TestSaveConfigCacheAtomicReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache")
	for _, value := range []string{"original contents", "new"} {
		if err := saveConfigCache(path, []byte(value)); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != value {
			t.Fatalf("cache = %q, err = %v", got, err)
		}
	}
	blocked := filepath.Join(filepath.Dir(path), "directory")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := saveConfigCache(blocked, []byte("new")); err == nil {
		t.Fatal("expected rename failure")
	}
	temporary, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".config-*"))
	if err != nil || len(temporary) != 0 {
		t.Fatalf("temporary cache files leaked: %v, %v", temporary, err)
	}
}
