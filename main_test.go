// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/czerwonk/ping_exporter/config"
	"github.com/prometheus/client_golang/prometheus"
)

func TestEnvironmentFlagProcess(t *testing.T) {
	if os.Getenv("PING_EXPORTER_TEST_FLAGS") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("PING_EXPORTER_TEST_ARGS")), &args); err != nil {
		t.Fatal(err)
	}
	if _, err := kingpin.CommandLine.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	values := make(map[string]interface{})
	for _, flag := range kingpin.CommandLine.Model().Flags {
		values[flag.Name] = flag.Value.String()
	}
	values["pushmetrics.url"] = *pushMetricsURLs
	values["pushmetrics.extraLabel"] = *pushMetricsExtraLabels
	values["targets"] = *targetFlag
	if err := json.NewEncoder(os.Stdout).Encode(values); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestEnvironmentFlags(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, env map[string]string, args []string, want map[string]interface{}, wantError bool) {
		t.Helper()
		encodedArgs, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(executable, "-test.run=^TestEnvironmentFlagProcess$")
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "PING_") && !strings.HasPrefix(entry, "CONFIG_FILE=") && !strings.HasPrefix(entry, "CMD_FLAGS=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "PING_EXPORTER_TEST_FLAGS=1", "PING_EXPORTER_TEST_ARGS="+string(encodedArgs))
		for key, value := range env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		output, err := cmd.CombinedOutput()
		if wantError {
			if err == nil {
				t.Fatalf("invalid environment accepted: %s", output)
			}
			return
		}
		if err != nil {
			t.Fatalf("parse flags: %v\n%s", err, output)
		}
		var got map[string]interface{}
		if err := json.Unmarshal(output, &got); err != nil {
			t.Fatalf("decode output: %v\n%s", err, output)
		}
		for name, value := range want {
			if !reflect.DeepEqual(got[name], value) {
				t.Errorf("%s = %#v, want %#v", name, got[name], value)
			}
		}
	}
	env := map[string]string{
		"PING_LISTEN_ADDRESS": "127.0.0.1:19427", "PING_METRICS_PATH": "/custom",
		"PING_METRICS_TOKEN": "test-token", "PING_TLS_ENABLED": "true",
		"PING_TLS_CERT_FILE": "/tmp/cert.pem", "PING_TLS_KEY_FILE": "/tmp/key.pem",
		"PING_TLS_MUTUAL_AUTH": "true", "PING_TLS_CA_FILE": "/tmp/ca.pem",
		"PING_CONFIG_FILE": "/tmp/config with spaces.yml", "CONFIG_FILE": "/tmp/legacy.yml",
		"PING_CONFIG_REFRESH": "2m", "PING_INTERVAL": "3s", "PING_TIMEOUT": "2s",
		"PING_SIZE": "64", "PING_FW_MARK": "7", "PING_HISTORY_SIZE": "25",
		"PING_DNS_REFRESH": "3m", "PING_DNS_NAMESERVER": "127.0.0.1:53", "PING_DNS_TIMEOUT": "1s",
		"PING_DISABLE_IPV6": "true", "PING_DISABLE_IPV4": "true", "PING_LOG_LEVEL": "debug",
		"PING_PUSHMETRICS_INTERVAL": "20s", "PING_PUSHMETRICS_TIMEOUT": "8s",
		"PING_PUSHMETRICS_BASE_DIR": "/tmp/data", "PING_PUSHMETRICS_QUEUE_DIR": "/tmp/queue",
		"PING_PUSHMETRICS_MAX_DISK_USAGE": "2048", "PING_PUSHMETRICS_MAX_INMEMORY": "0",
		"PING_METRICS_DEPRECATED": "enable", "PING_METRICS_RTTUNIT": "both",
		"PING_PUSHMETRICS_URLS":   " http://first/import ; https://second/import ",
		"PING_PUSHMETRICS_LABELS": ` site="test site" ; job="ping" `,
		"PING_TARGETS":            "127.0.0.1  ::1",
	}
	t.Run("all documented variables", func(t *testing.T) {
		run(t, env, nil, map[string]interface{}{
			"web.listen-address": "127.0.0.1:19427", "web.telemetry-path": "/custom",
			"web.token": "test-token", "web.tls.enabled": "true",
			"web.tls.cert-file": "/tmp/cert.pem", "web.tls.key-file": "/tmp/key.pem",
			"web.tls.mutual-auth-enabled": "true", "web.tls.ca-file": "/tmp/ca.pem",
			"config.path": "/tmp/config with spaces.yml", "config.refresh": "2m0s",
			"ping.interval": "3s", "ping.timeout": "2s", "ping.size": "64",
			"ping.fw-mark": "7", "ping.history-size": "25", "dns.refresh": "3m0s",
			"dns.nameserver": "127.0.0.1:53", "dns.timeout": "1s",
			"options.disable-ipv6": "true", "options.disable-ipv4": "true", "log.level": "debug",
			"pushmetrics.interval": "20s", "pushmetrics.timeout": "8s",
			"pushmetrics.base-dir": "/tmp/data", "pushmetrics.queue-dir": "/tmp/queue",
			"pushmetrics.max-disk-usage": "2048", "pushmetrics.max-inmemory-requests": "0",
			"metrics.deprecated": "enable", "metrics.rttunit": "both",
			"pushmetrics.url":        []interface{}{"http://first/import", "https://second/import"},
			"pushmetrics.extraLabel": []interface{}{`site="test site"`, `job="ping"`},
			"targets":                []interface{}{"127.0.0.1", "::1"},
		}, false)
	})
	t.Run("command line wins", func(t *testing.T) {
		run(t, env, []string{
			"--config.path=/cli.yml", "--web.listen-address=:9999", "--no-web.tls.enabled",
			"--config.refresh=5s", "--pushmetrics.url=http://cli/a;b",
			"--pushmetrics.url=http://cli/second", `--pushmetrics.extraLabel=site="cli"`, "localhost",
		}, map[string]interface{}{
			"config.path": "/cli.yml", "web.listen-address": ":9999", "web.tls.enabled": "false",
			"config.refresh": "5s", "pushmetrics.url": []interface{}{"http://cli/a;b", "http://cli/second"},
			"pushmetrics.extraLabel": []interface{}{`site="cli"`}, "targets": []interface{}{"localhost"},
		}, false)
	})
	t.Run("legacy config variable", func(t *testing.T) {
		run(t, map[string]string{"CONFIG_FILE": "/legacy.yml"}, nil, map[string]interface{}{"config.path": "/legacy.yml"}, false)
	})
	t.Run("unchanged defaults", func(t *testing.T) {
		run(t, nil, nil, map[string]interface{}{
			"config.path": "", "web.listen-address": ":9427", "config.refresh": "1m0s",
			"ping.timeout": "4s", "pushmetrics.url": nil, "targets": nil,
		}, false)
	})
	for _, name := range []string{"PING_CONFIG_REFRESH", "PING_TLS_ENABLED", "PING_PUSHMETRICS_MAX_DISK_USAGE", "PING_PUSHMETRICS_MAX_INMEMORY"} {
		t.Run("invalid "+name, func(t *testing.T) {
			run(t, map[string]string{name: "invalid"}, nil, nil, true)
		})
	}
}

func preserveConfigFlags(t *testing.T) {
	t.Helper()
	path, refresh, client, targets, baseDir := *configFile, *configRefresh, configHTTPClient, *targetFlag, *pushMetricsBaseDir
	t.Cleanup(func() {
		*configFile, *configRefresh, configHTTPClient, *targetFlag = path, refresh, client, targets
		*pushMetricsBaseDir = baseDir
	})
	*targetFlag = nil
	*pushMetricsBaseDir = t.TempDir()
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
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
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
		if err := watchConfig(context.Background(), nil, nil, nil, nil, cfg); err != nil {
			t.Fatal(err)
		}
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
		if _, err := loadConfigContext(ctx, true); err == nil {
			t.Fatal("expected request cancellation")
		}
	})
}

func TestWatchHTTPConfig(t *testing.T) {
	preserveConfigFlags(t)
	*configRefresh = 5 * time.Millisecond
	const original = "targets: [127.0.0.1, 127.0.0.2]"
	const changed = "labels: {location: CN}\ntargets:\n  - host: 127.0.0.1\n    site: updated\n  - 127.0.0.3\n"
	const removed = "targets: [127.0.0.3]"
	responses := []struct {
		status int
		body   string
	}{
		{http.StatusOK, original}, // Initial load.
		{http.StatusOK, original}, // No change.
		{http.StatusServiceUnavailable, "unavailable"},
		{http.StatusOK, "targets: ["},
		{http.StatusOK, "{}"},
		{http.StatusOK, changed}, // Applying this update fails once.
		{http.StatusOK, changed}, // Retry the same update.
		{http.StatusOK, changed}, // Already applied; do not reload.
		{http.StatusOK, removed},
		{http.StatusOK, "labels: {location: JP}\ntargets: []"},
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
			if len(cfg.Targets) == 0 {
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
	if len(updates) != 4 {
		t.Fatalf("reload count = %d, want 4 (failed attempt, retry, removal, clear)", len(updates))
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
	if updates[1].Labels["location"] != "CN" || updates[3].Labels["location"] != "JP" || updates[3].Targets == nil || len(updates[3].Targets) != 0 {
		t.Fatalf("global labels or explicit empty target update lost: %#v", updates[3])
	}
	if got := requests.Load(); got != int32(len(responses)) {
		t.Fatalf("requests = %d, want %d", got, len(responses))
	}
}

func TestExplicitEmptyTargets(t *testing.T) {
	preserveConfigFlags(t)
	*targetFlag = []string{"127.0.0.1"}
	cfg, err := config.FromYAML(strings.NewReader("labels: {location: CN}\ntargets: []"))
	if err != nil {
		t.Fatal(err)
	}

	addFlagToConfig(cfg)
	if cfg.Targets == nil || len(cfg.Targets) != 0 {
		t.Fatalf("explicit empty targets replaced by CLI targets: %#v", cfg.Targets)
	}
}

func TestStartServerWaitsForPersistence(t *testing.T) {
	urls, dir, capacity := *pushMetricsURLs, *pushMetricsQueueDir, *pushMetricsMaxInmemory
	interval, timeout, maxBytes := *pushMetricsInterval, *pushMetricsTimeout, *pushMetricsMaxDiskUsage
	address, tlsEnabled, path := *listenAddress, *serverUseTLS, *metricsPath
	t.Cleanup(func() {
		*pushMetricsURLs, *pushMetricsQueueDir, *pushMetricsMaxInmemory = urls, dir, capacity
		*pushMetricsInterval, *pushMetricsTimeout, *pushMetricsMaxDiskUsage = interval, timeout, maxBytes
		*listenAddress, *serverUseTLS, *metricsPath = address, tlsEnabled, path
	})
	*pushMetricsQueueDir = t.TempDir()
	*pushMetricsMaxInmemory, *pushMetricsMaxDiskUsage = 100, 1<<20
	*pushMetricsInterval, *pushMetricsTimeout = time.Hour, time.Hour
	*listenAddress, *serverUseTLS, *metricsPath = "127.0.0.1:0", false, "/metrics"
	started := make(chan struct{}, 1)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the upload before blocking so cancellation reaches the server.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer remote.Close()
	*pushMetricsURLs = []string{remote.URL}
	collector := prometheus.NewGauge(prometheus.GaugeOpts{Name: "shutdown_test", Help: "Test metric"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- startServer(ctx, collector) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not start pushing")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down")
	}
	queue, err := openMetricsQueue(filepath.Join(*pushMetricsQueueDir, "0"), 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	assertMetricsQueueState(t, queue, 0, 1)

	// Listener failures must return normally so persistence defers can run.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	*listenAddress = listener.Addr().String()
	if err := startServer(context.Background(), collector); err == nil {
		t.Fatal("expected listener error")
	}
}
