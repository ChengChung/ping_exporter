// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/czerwonk/ping_exporter/config"
)

func preserveConfigFlags(t *testing.T) {
	t.Helper()
	path, refresh, client, targets := *configFile, *configRefresh, configHTTPClient, *targetFlag
	t.Cleanup(func() {
		*configFile, *configRefresh, configHTTPClient, *targetFlag = path, refresh, client, targets
	})
	*targetFlag = nil
}

func TestLoadConfigSources(t *testing.T) {
	preserveConfigFlags(t)
	const body = "targets:\n  - host: 127.0.0.1\n    site: local\n"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("key") != "value" {
			t.Errorf("unexpected config request: %s %s", r.Method, r.URL)
		}
		fmt.Fprint(w, body)
	})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	tlsServer := httptest.NewTLSServer(handler)
	defer tlsServer.Close()

	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		source string
		client *http.Client
	}{
		{"local", path, httpServer.Client()},
		{"http", httpServer.URL + "/config.yml?key=value", httpServer.Client()},
		{"https", tlsServer.URL + "/config.yml?key=value", tlsServer.Client()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*configFile = tc.source
			configHTTPClient = tc.client
			cfg, err := loadConfig()
			if err != nil {
				t.Fatal(err)
			}
			want := []config.TargetConfig{{Addr: "127.0.0.1", Labels: map[string]string{"site": "local"}}}
			if !reflect.DeepEqual(cfg.Targets, want) {
				t.Fatalf("targets = %#v, want %#v", cfg.Targets, want)
			}
			if cfg.Ping.History != *historySize || cfg.Ping.Interval.Duration() != *pingInterval {
				t.Fatal("command-line defaults were not applied")
			}
		})
	}

	t.Run("flags only", func(t *testing.T) {
		*configFile = ""
		*targetFlag = []string{"127.0.0.2"}
		cfg, err := loadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Targets) != 1 || cfg.Targets[0].Addr != "127.0.0.2" {
			t.Fatalf("unexpected flag targets: %#v", cfg.Targets)
		}
		// With no configuration source there is nothing to watch.
		watchConfig(nil, nil, nil, nil, cfg)
	})
}

func TestLoadHTTPConfigErrors(t *testing.T) {
	preserveConfigFlags(t)
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"not found", http.StatusNotFound, "targets: [127.0.0.1]", "HTTP 404"},
		{"server error", http.StatusInternalServerError, "error", "HTTP 500"},
		{"no content", http.StatusNoContent, "", "HTTP 204"},
		{"invalid YAML", http.StatusOK, "targets: [", "failed to decode YAML"},
		{"empty body", http.StatusOK, "", "failed to decode YAML"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			*configFile = server.URL
			if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}

	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer server.Close()
		*configFile = server.URL
		configHTTPClient = &http.Client{Timeout: 20 * time.Millisecond}
		if _, err := loadConfig(); err == nil {
			t.Fatal("expected request timeout")
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := loadConfigContext(ctx); err == nil {
			t.Fatal("expected request cancellation")
		}
	})
}

func TestWatchHTTPConfig(t *testing.T) {
	preserveConfigFlags(t)
	*configRefresh = 5 * time.Millisecond
	const original = "targets: [127.0.0.1, 127.0.0.2]"
	const changed = "targets:\n  - host: 127.0.0.1\n    site: updated\n  - 127.0.0.3\n"
	const removed = "targets: [127.0.0.3]"
	responses := []struct {
		status int
		body   string
	}{
		{http.StatusOK, original}, // Initial load.
		{http.StatusOK, original}, // No change.
		{http.StatusServiceUnavailable, "unavailable"},
		{http.StatusOK, "targets: ["},
		{http.StatusOK, "targets: []"},
		{http.StatusOK, changed}, // Applying this update fails once.
		{http.StatusOK, changed}, // Retry the same update.
		{http.StatusOK, changed}, // Already applied; do not reload.
		{http.StatusOK, removed},
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := int(requests.Add(1)) - 1
		if i >= len(responses) {
			i = len(responses) - 1
		}
		w.WriteHeader(responses[i].status)
		fmt.Fprint(w, responses[i].body)
	}))
	defer server.Close()
	*configFile = server.URL
	initial, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var updates []*config.Config
	go func() {
		defer close(done)
		watchHTTPConfig(ctx, initial, func(cfg *config.Config) error {
			updates = append(updates, cfg)
			if len(updates) == 1 {
				return fmt.Errorf("temporary reload failure")
			}
			if len(cfg.Targets) == 1 {
				cancel()
			}
			return nil
		})
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		cancel()
		<-done
		t.Fatal("HTTP configuration watcher did not apply updates")
	}
	if len(updates) != 3 {
		t.Fatalf("reload count = %d, want 3 (failed attempt, retry, removal)", len(updates))
	}
	if !reflect.DeepEqual(updates[0], updates[1]) {
		t.Fatal("failed reload was not retried with the same configuration")
	}
	if updates[1].Targets[1].Addr != "127.0.0.3" || updates[1].Targets[0].Labels["site"] != "updated" {
		t.Fatalf("target addition or label change was lost: %#v", updates[1].Targets)
	}
	if updates[2].Targets[0].Addr != "127.0.0.3" {
		t.Fatalf("unexpected targets after removal: %#v", updates[2].Targets)
	}
	if got := requests.Load(); got != int32(len(responses)) {
		t.Fatalf("requests = %d, want %d", got, len(responses))
	}
}
