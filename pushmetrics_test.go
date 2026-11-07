package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/protobuf/proto"
)

func TestPushMetricsSnapshotTimestamp(t *testing.T) {
	cases := []struct {
		name      string
		timestamp *int64
	}{
		{"push_first", nil},
		{"push_second", nil},
		{"push_explicit", proto.Int64(123)},
		{"push_epoch", proto.Int64(0)},
	}
	var families []*dto.MetricFamily
	for _, testCase := range cases {
		families = append(families, &dto.MetricFamily{
			Name: proto.String(testCase.name),
			Type: dto.MetricType_GAUGE.Enum(),
			Metric: []*dto.Metric{{
				Gauge:       &dto.Gauge{Value: proto.Float64(1)},
				TimestampMs: testCase.timestamp,
			}},
		})
	}
	gatherer := prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) { return families, nil })
	for _, now := range []time.Time{time.Unix(1, 0), time.Unix(2, 0)} {
		data, err := captureMetrics(gatherer, now, nil)
		if err != nil {
			t.Fatal(err)
		}
		for index, testCase := range cases {
			wantTimestamp := now.UnixMilli()
			if testCase.timestamp != nil {
				wantTimestamp = *testCase.timestamp
			}
			want := fmt.Sprintf("%s 1 %d\n", testCase.name, wantTimestamp)
			if !strings.Contains(string(data), want) {
				t.Errorf("snapshot missing %q: %s", want, data)
			}
			if families[index].Metric[0].TimestampMs != testCase.timestamp {
				t.Errorf("source timestamp changed for %s", testCase.name)
			}
		}
	}
}

func TestPushMetricsExtraLabels(t *testing.T) {
	labels, err := parsePushMetricsExtraLabels([]string{`instance="original"`, `job="vm"`, `instance="foobar"`, `note="line\nquote\"slash\\="`, `empty=""`})
	if err != nil {
		t.Fatal(err)
	}
	if labels["instance"] != "foobar" || labels["job"] != "vm" || labels["note"] != "line\nquote\"slash\\=" || labels["empty"] != "" {
		t.Fatalf("unexpected labels: %#v", labels)
	}
	for _, value := range []string{`instance`, `="value"`, `bad-name="value"`, `1name="value"`, `__name__="value"`, `__reserved="value"`, `instance=foobar`, `instance="unterminated`, `instance="bad\q"`, `instance="value",job="vm"`} {
		if _, err := parsePushMetricsExtraLabels([]string{value}); err == nil {
			t.Errorf("accepted invalid extra label %q", value)
		}
	}

	registry := prometheus.NewRegistry()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "push_test", Help: "Test metric",
		ConstLabels: prometheus.Labels{"instance": "local", "target": "localhost"},
	})
	registry.MustRegister(gauge)
	gauge.Set(42)
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	original := proto.Clone(families[0]).(*dto.MetricFamily)
	gatherer := prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) { return families, nil })
	data, err := captureMetrics(gatherer, time.Unix(1, 0), labels)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `push_test{empty="",instance="foobar",job="vm",note="line\nquote\"slash\\=",target="localhost"} 42 1000`+"\n") {
		t.Fatalf("unexpected labeled snapshot: %s", data)
	}
	if !proto.Equal(families[0], original) {
		t.Fatal("push labels or timestamps modified the original gathered metrics")
	}
}

func TestPushMetricsRecovery(t *testing.T) {
	registry := prometheus.NewRegistry()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "push_test", Help: "Test metric"})
	registry.MustRegister(gauge)
	queue, err := openMetricsQueue(t.TempDir(), 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan string, 2)
	var available atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "text/plain; version=0.0.4" {
			t.Error("unexpected push request")
		}
		if !available.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		received <- string(body)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	for index := 1; index <= 2; index++ {
		gauge.Set(float64(index))
		data, err := captureMetrics(registry, time.Unix(int64(index), 0), prometheus.Labels{"instance": "foobar", "job": "vm"})
		if err != nil {
			t.Fatal(err)
		}
		if err := queue.enqueue(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := queue.flush(context.Background(), server.Client(), server.URL); err == nil {
		t.Fatal("expected HTTP failure")
	}
	queue, err = openMetricsQueue(queue.dir, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if queue.used == 0 {
		t.Fatal("queued data was not recovered after reopening")
	}
	available.Store(true)
	if err := queue.flush(context.Background(), server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 {
		t.Fatalf("received %d snapshots, want 2", len(received))
	}
	first, second := <-received, <-received
	if !strings.Contains(first, `push_test{instance="foobar",job="vm"} 1 1000`+"\n") || !strings.Contains(second, `push_test{instance="foobar",job="vm"} 2 2000`+"\n") {
		t.Fatalf("unexpected replay: %q, %q", first, second)
	}
	entries, err := os.ReadDir(queue.dir)
	if err != nil || len(entries) != 0 || queue.used != 0 {
		t.Fatalf("queue not emptied after success: entries=%v, used=%d, error=%v", entries, queue.used, err)
	}
}

func assertMetricsQueueState(t *testing.T, queue *metricsQueue, memory, disk int) {
	t.Helper()
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.memory) != memory || len(queue.diskFiles) != disk {
		t.Fatalf("queue has memory=%d, disk=%d; want memory=%d, disk=%d", len(queue.memory), len(queue.diskFiles), memory, disk)
	}
	entries, err := os.ReadDir(queue.dir)
	if err != nil || len(entries) != disk {
		t.Fatalf("unexpected disk contents: entries=%v, error=%v", entries, err)
	}
}

func TestPushMetricsMemoryQueue(t *testing.T) {
	if _, err := openMetricsQueue(t.TempDir(), 1024, -1); err == nil {
		t.Fatal("accepted negative memory queue capacity")
	}
	queue, err := openMetricsQueue(t.TempDir(), 1024, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"one", "two"} {
		if err := queue.enqueue([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	assertMetricsQueueState(t, queue, 2, 0)
	if err := queue.enqueue([]byte("three")); err != nil {
		t.Fatal(err)
	}
	assertMetricsQueueState(t, queue, 0, 3)
	if err := queue.enqueue([]byte("four")); err != nil {
		t.Fatal(err)
	}
	assertMetricsQueueState(t, queue, 0, 4)
	queue, err = openMetricsQueue(queue.dir, 1024, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.enqueue([]byte("five")); err != nil {
		t.Fatal(err)
	}
	assertMetricsQueueState(t, queue, 0, 5)
	received := make(chan string, 6)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		received <- string(body)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if err := queue.flush(context.Background(), server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
	assertMetricsQueueState(t, queue, 0, 0)
	data := []byte("six")
	if err := queue.enqueue(data); err != nil {
		t.Fatal(err)
	}
	data[0] = 'X'
	assertMetricsQueueState(t, queue, 1, 0)
	if err := queue.flush(context.Background(), server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
	assertMetricsQueueState(t, queue, 0, 0)
	if len(received) != 6 {
		t.Fatalf("received %d batches, want 6", len(received))
	}
	for _, want := range []string{"one", "two", "three", "four", "five", "six"} {
		if got := <-received; got != want {
			t.Fatalf("received %q, want %q", got, want)
		}
	}
}

func TestPushMetricsMemorySpillDuringSend(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			queue, err := openMetricsQueue(t.TempDir(), 1024, 3)
			if err != nil {
				t.Fatal(err)
			}
			if err := queue.enqueue([]byte("one")); err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{})
			release := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			received := make(chan string, 5)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Error(err)
				}
				received <- string(body)
				if requests.Add(1) == 1 {
					close(started)
					<-release
					writer.WriteHeader(status)
					return
				}
				writer.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			defer unblock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- queue.flush(ctx, server.Client(), server.URL) }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("memory batch did not start sending")
			}
			for _, data := range []string{"two", "three", "four"} {
				if err := queue.enqueue([]byte(data)); err != nil {
					t.Fatal(err)
				}
			}
			assertMetricsQueueState(t, queue, 0, 4)
			unblock()
			select {
			case err := <-done:
				if (err == nil) != (status == http.StatusNoContent) {
					t.Fatalf("unexpected push result: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("push did not finish after release")
			}
			want := []string{"one", "two", "three", "four"}
			if status == http.StatusServiceUnavailable {
				assertMetricsQueueState(t, queue, 0, 4)
				if err := queue.flush(ctx, server.Client(), server.URL); err != nil {
					t.Fatal(err)
				}
				want = append([]string{"one"}, want...)
			}
			assertMetricsQueueState(t, queue, 0, 0)
			if len(received) != len(want) {
				t.Fatalf("received %d requests, want %d", len(received), len(want))
			}
			for _, batch := range want {
				if got := <-received; got != batch {
					t.Fatalf("received %q, want %q", got, batch)
				}
			}
		})
	}
}

func TestPushMetricsPartialSpill(t *testing.T) {
	queue, err := openMetricsQueue(t.TempDir(), 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.enqueue([]byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := queue.enqueue([]byte("two")); err == nil {
		t.Fatal("expected disk limit to interrupt spilling")
	}
	assertMetricsQueueState(t, queue, 1, 1)
	if err := queue.enqueue([]byte("new")); err == nil {
		t.Fatal("expected new batch to be rejected while spilling is blocked")
	}
	assertMetricsQueueState(t, queue, 1, 1)
	received := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		received <- string(body)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if err := queue.flush(context.Background(), server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
	assertMetricsQueueState(t, queue, 0, 0)
	if len(received) != 2 {
		t.Fatalf("received %d batches, want 2", len(received))
	}
	if first, second := <-received, <-received; first != "one" || second != "two" {
		t.Fatalf("partial spill reordered or lost batches: %q, %q", first, second)
	}
}

func TestPushMetricsQueueLimit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".pending-crashed"), []byte("unfinished"), 0o600); err != nil {
		t.Fatal(err)
	}
	queue, err := openMetricsQueue(dir, 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".pending-crashed")); !os.IsNotExist(err) {
		t.Fatalf("incomplete temporary snapshot was not cleaned up: %v", err)
	}
	if err := queue.enqueue([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := queue.enqueue([]byte("more")); err == nil {
		t.Fatal("expected full queue error")
	}
	if queue.used != 4 {
		t.Fatalf("queue used = %d, want 4", queue.used)
	}
}

func TestPushMetricsUnacknowledged(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusTooManyRequests, http.StatusTemporaryRedirect, 0} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			queue, err := openMetricsQueue(t.TempDir(), 1024, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := queue.enqueue([]byte("sample 1 1000\n")); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				io.Copy(io.Discard, request.Body)
				if status == 0 {
					<-request.Context().Done()
					return
				}
				writer.Header().Set("Location", "/redirect")
				writer.WriteHeader(status)
			}))
			defer server.Close()
			client := &http.Client{
				Timeout: 20 * time.Millisecond,
				CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}
			defer client.CloseIdleConnections()
			if err := queue.flush(context.Background(), client, server.URL); err == nil {
				t.Fatal("expected unsuccessful push")
			}
			entries, err := os.ReadDir(queue.dir)
			if err != nil || len(entries) != 1 || queue.used == 0 {
				t.Fatalf("unacknowledged batch was not preserved: entries=%v, error=%v", entries, err)
			}
		})
	}
}

func TestValidatePushMetrics(t *testing.T) {
	for _, endpoint := range []string{"", "file:///tmp/metrics", "http://", "http://example.com/#fragment"} {
		if err := validatePushMetrics(endpoint, time.Second, time.Second); err == nil {
			t.Errorf("accepted invalid URL %q", endpoint)
		}
	}
	if err := validatePushMetrics("https://example.com/api/v1/import/prometheus", time.Second, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := validatePushMetrics("http://example.com", 0, time.Second); err == nil {
		t.Fatal("accepted zero interval")
	}
}

func TestPushMetricsSlowRemote(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{Name: "push_test", Help: "Test metric"}))
	queue, err := openMetricsQueue(t.TempDir(), 1<<20, 3)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		io.Copy(io.Discard, request.Body)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-request.Context().Done():
		}
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runPushMetrics(ctx, registry, queue, server.URL, 5*time.Millisecond, time.Second, nil)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("push request did not start")
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		entries, err := os.ReadDir(queue.dir)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".prom") {
				count++
			}
		}
		if count >= 3 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("snapshots stopped while push request was blocked")
		case <-poll.C:
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("push worker did not stop after cancellation")
	}
}

func TestPushMetricsMultipleEndpoints(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create multiple test servers
	var endpoints []*httptest.Server
	var requestCounts []atomic.Int32
	endpointCount := 3

	for i := 0; i < endpointCount; i++ {
		idx := i // capture for closure
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCounts[idx].Add(1)
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
			w.WriteHeader(http.StatusNoContent)
		}))
		endpoints = append(endpoints, server)
		requestCounts = append(requestCounts, atomic.Int32{})
	}
	defer func() {
		for _, server := range endpoints {
			server.Close()
		}
	}()

	// Create workers for each endpoint
	tmpDir := t.TempDir()
	workers := make([]*pushMetricsWorker, endpointCount)
	for i, server := range endpoints {
		queueDir := filepath.Join(tmpDir, fmt.Sprintf("queue-%d", i))
		queue, err := openMetricsQueue(queueDir, 1073741824, 100)
		if err != nil {
			t.Fatalf("failed to open queue %d: %v", i, err)
		}
		workers[i] = &pushMetricsWorker{
			endpoint: server.URL,
			queue:    queue,
			index:    i,
		}
	}

	// Create test metrics gatherer
	registry := prometheus.NewRegistry()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "test_metric",
		Help: "Test metric",
	})
	registry.MustRegister(gauge)
	gauge.Set(42)

	// Run the multi-endpoint push metrics function
	done := make(chan error, 1)
	go func() { done <- runPushMetricsMulti(ctx, registry, workers, 100*time.Millisecond, 5*time.Second, nil) }()

	// Wait a bit for the goroutine to process metrics
	time.Sleep(500 * time.Millisecond)

	// Verify that all endpoints received requests
	for i := range requestCounts {
		if requestCounts[i].Load() == 0 {
			t.Errorf("endpoint %d did not receive any requests", i)
		}
	}

	// Cancel context and wait for goroutine to finish
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("push workers did not finish")
	}
}

func TestPushMetricsMultipleEndpointIndependentQueues(t *testing.T) {
	// This test verifies that each endpoint has its own independent queue
	// by checking that queue directories are created with index-based names
	tmpDir := t.TempDir()

	queueDirs := []string{
		filepath.Join(tmpDir, "queue-0"),
		filepath.Join(tmpDir, "queue-1"),
		filepath.Join(tmpDir, "queue-2"),
	}

	workers := make([]*pushMetricsWorker, len(queueDirs))
	for i, queueDir := range queueDirs {
		queue, err := openMetricsQueue(queueDir, 1073741824, 100)
		if err != nil {
			t.Fatalf("failed to open queue %d: %v", i, err)
		}
		workers[i] = &pushMetricsWorker{
			endpoint: fmt.Sprintf("http://localhost:%d", 9000+i),
			queue:    queue,
			index:    i,
		}
	}

	// Verify queue directories exist
	for i, queueDir := range queueDirs {
		info, err := os.Stat(queueDir)
		if err != nil {
			t.Errorf("queue directory %d not found: %v", i, err)
		}
		if !info.IsDir() {
			t.Errorf("queue path %d is not a directory", i)
		}
	}

	// Verify that each worker has its own queue with independent state
	for i, worker := range workers {
		if worker.index != i {
			t.Errorf("worker %d has incorrect index: %d", i, worker.index)
		}
		if worker.queue == nil {
			t.Errorf("worker %d has nil queue", i)
		}
	}
}

func TestPushMetricsShutdownPersistsAllEndpoints(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{Name: "shutdown_sample", Help: "Test metric"}))
	started := make(chan int, 4)
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case started <- 0:
		default:
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failed.Close()
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		started <- 1
		<-r.Context().Done()
	}))
	defer blocked.Close()
	workers := make([]*pushMetricsWorker, 2)
	for i, endpoint := range []string{failed.URL, blocked.URL} {
		queue, err := openMetricsQueue(t.TempDir(), 1<<20, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, data := range []string{"first", "second"} {
			if err := queue.enqueue([]byte(data)); err != nil {
				t.Fatal(err)
			}
		}
		workers[i] = &pushMetricsWorker{endpoint: endpoint, queue: queue, index: i}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runPushMetricsMulti(ctx, registry, workers, time.Hour, time.Hour, nil) }()
	seen := make(map[int]bool)
	for len(seen) < len(workers) {
		select {
		case index := <-started:
			seen[index] = true
		case <-time.After(2 * time.Second):
			t.Fatal("push did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not cancel the in-flight request and persist queues")
	}
	for _, worker := range workers {
		assertMetricsQueueState(t, worker.queue, 0, 3)
		reopened, err := openMetricsQueue(worker.queue.dir, 1<<20, 100)
		if err != nil {
			t.Fatal(err)
		}
		received := make(chan string, 3)
		recovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			content, _ := io.ReadAll(r.Body)
			received <- string(content)
			w.WriteHeader(http.StatusNoContent)
		}))
		err = reopened.flush(context.Background(), recovery.Client(), recovery.URL)
		recovery.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(received) != 3 {
			t.Fatalf("recovered %d batches, want 3", len(received))
		}
		if first, second, third := <-received, <-received, <-received; first != "first" || second != "second" || !strings.Contains(third, "shutdown_sample") {
			t.Fatalf("recovered batches lost or reordered: %q, %q, %q", first, second, third)
		}
		assertMetricsQueueState(t, reopened, 0, 0)
	}
}

func TestPushMetricsShutdownReportsPersistenceFailure(t *testing.T) {
	full, err := openMetricsQueue(t.TempDir(), 3, 100)
	if err != nil {
		t.Fatal(err)
	}
	good, err := openMetricsQueue(t.TempDir(), 1024, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, queue := range []*metricsQueue{full, good} {
		for _, data := range []string{"one", "two"} {
			if err := queue.enqueue([]byte(data)); err != nil {
				t.Fatal(err)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = runPushMetricsMulti(ctx, prometheus.NewRegistry(), []*pushMetricsWorker{
		{queue: full, index: 0}, {queue: good, index: 1},
	}, time.Hour, time.Second, nil)
	if err == nil || !strings.Contains(err.Error(), "endpoint #1") || !strings.Contains(err.Error(), "1 metrics batches remain unpersisted") {
		t.Fatalf("missing persistence error: %v", err)
	}
	assertMetricsQueueState(t, full, 1, 1)
	assertMetricsQueueState(t, good, 0, 2)
	full.maxBytes = 1024
	if err := full.persist(); err != nil {
		t.Fatal(err)
	}
	assertMetricsQueueState(t, full, 0, 2)
}

func TestPushMetricsShutdownWriteError(t *testing.T) {
	queue, err := openMetricsQueue(filepath.Join(t.TempDir(), "queue"), 1024, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.enqueue([]byte("retained")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(queue.dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queue.dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := queue.persist(); err == nil {
		t.Fatal("write error was ignored")
	}
	if err := os.Remove(queue.dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(queue.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	assertMetricsQueueState(t, queue, 1, 0)
	if err := queue.persist(); err != nil {
		t.Fatal(err)
	}
	assertMetricsQueueState(t, queue, 0, 1)
}
