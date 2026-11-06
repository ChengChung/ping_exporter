package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

var pushMetricsLabelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func parsePushMetricsExtraLabels(values []string) (prometheus.Labels, error) {
	labels := make(prometheus.Labels, len(values))
	for index, value := range values {
		name, quoted, found := strings.Cut(value, "=")
		if !found || !pushMetricsLabelName.MatchString(name) || strings.HasPrefix(name, "__") || !strings.HasPrefix(quoted, `"`) {
			return nil, fmt.Errorf("pushmetrics.extraLabel #%d must have the form name=\"value\" with a non-reserved label name", index+1)
		}
		decoded, err := strconv.Unquote(quoted)
		if err != nil {
			return nil, fmt.Errorf("invalid quoted value in pushmetrics.extraLabel #%d: %w", index+1, err)
		}
		labels[name] = decoded
	}
	return labels, nil
}

type metricsBatch struct {
	data []byte
	path string
	size int64
}

type metricsQueue struct {
	dir            string
	maxBytes       int64
	used           int64
	sequence       int64
	memory         []*metricsBatch
	memoryCapacity int
	diskFiles      []string
	diskMode       bool
	mu             sync.Mutex
	flushMu        sync.Mutex
}

func openMetricsQueue(dir string, maxBytes int64, memoryCapacity int) (*metricsQueue, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("pushmetrics.max-disk-usage must be greater than 0")
	}
	if memoryCapacity < 0 {
		return nil, fmt.Errorf("pushmetrics.max-inmemory-requests must not be negative")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	queue := &metricsQueue{dir: dir, maxBytes: maxBytes, memoryCapacity: memoryCapacity}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".pending-") {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return nil, err
			}
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".prom") {
			continue
		}
		sequence, err := strconv.ParseInt(strings.TrimSuffix(entry.Name(), ".prom"), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid metrics queue filename %q", entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		queue.used += info.Size()
		queue.diskFiles = append(queue.diskFiles, filepath.Join(dir, entry.Name()))
		if sequence > queue.sequence {
			queue.sequence = sequence
		}
	}
	queue.diskMode = len(queue.diskFiles) > 0
	return queue, nil
}

func (queue *metricsQueue) enqueue(data []byte) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if !queue.diskMode && queue.memoryCapacity > 0 && len(queue.memory) < queue.memoryCapacity {
		queue.memory = append(queue.memory, &metricsBatch{data: bytes.Clone(data), size: int64(len(data))})
		if len(queue.memory) < queue.memoryCapacity {
			return nil
		}
		queue.diskMode = true
		return queue.spillMemoryLocked()
	}
	queue.diskMode = true
	if err := queue.spillMemoryLocked(); err != nil {
		return err
	}
	if _, err := queue.writeDiskLocked(data); err != nil {
		return err
	}
	queue.diskMode = true
	return nil
}

func (queue *metricsQueue) spillMemoryLocked() error {
	for len(queue.memory) > 0 {
		batch := queue.memory[0]
		path, err := queue.writeDiskLocked(batch.data)
		if err != nil {
			return err
		}
		batch.path = path
		batch.data = nil
		queue.memory[0] = nil
		queue.memory = queue.memory[1:]
	}
	return nil
}

func (queue *metricsQueue) writeDiskLocked(data []byte) (string, error) {
	if int64(len(data)) > queue.maxBytes-queue.used {
		return "", fmt.Errorf("metrics disk queue is full (%d bytes)", queue.maxBytes)
	}
	file, err := os.CreateTemp(queue.dir, ".pending-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	queue.sequence++
	name := fmt.Sprintf("%020d.prom", queue.sequence)
	path := filepath.Join(queue.dir, name)
	if err := os.Rename(file.Name(), path); err != nil {
		return "", err
	}
	queue.used += int64(len(data))
	queue.diskFiles = append(queue.diskFiles, path)
	return path, nil
}

func (queue *metricsQueue) nextBatch() (*metricsBatch, io.ReadCloser, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.diskFiles) == 0 {
		queue.diskMode = false
	}
	if !queue.diskMode {
		if len(queue.memory) == 0 {
			return nil, nil, nil
		}
		batch := queue.memory[0]
		return batch, io.NopCloser(bytes.NewReader(batch.data)), nil
	}
	path := queue.diskFiles[0]
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	return &metricsBatch{path: path, size: info.Size()}, file, nil
}

func (queue *metricsQueue) flush(ctx context.Context, client *http.Client, endpoint string) error {
	queue.flushMu.Lock()
	defer queue.flushMu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch, body, err := queue.nextBatch()
		if err != nil {
			return err
		}
		if batch == nil {
			return nil
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
		if err != nil {
			body.Close()
			return fmt.Errorf("create metrics push request: %w", err)
		}
		request.ContentLength = batch.size
		request.Header.Set("Content-Type", "text/plain; version=0.0.4")
		response, sendErr := client.Do(request)
		body.Close()
		if sendErr != nil {
			if requestErr, ok := sendErr.(*url.Error); ok {
				return fmt.Errorf("metrics push request failed: %w", requestErr.Err)
			}
			return fmt.Errorf("metrics push request failed")
		}
		_, drainErr := io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("metrics push returned HTTP %d", response.StatusCode)
		}
		if drainErr != nil {
			return fmt.Errorf("read metrics push response: %w", drainErr)
		}
		queue.mu.Lock()
		if batch.path != "" {
			err = os.Remove(batch.path)
			if err == nil {
				queue.used -= batch.size
				queue.diskFiles[0] = ""
				queue.diskFiles = queue.diskFiles[1:]
			}
		} else {
			queue.memory[0] = nil
			queue.memory = queue.memory[1:]
		}
		if len(queue.diskFiles) == 0 && len(queue.memory) == 0 {
			queue.diskMode = false
		}
		queue.mu.Unlock()
		if err != nil {
			return err
		}
	}
}

func captureMetrics(gatherer prometheus.Gatherer, now time.Time, extraLabels prometheus.Labels) ([]byte, error) {
	families, err := gatherer.Gather()
	if err != nil {
		return nil, err
	}
	timestamp := now.UnixMilli()
	var data bytes.Buffer
	for _, family := range families {
		family = proto.Clone(family).(*dto.MetricFamily)
		for _, metric := range family.Metric {
			for name, value := range extraLabels {
				found := false
				for _, label := range metric.Label {
					if label.GetName() == name {
						label.Value = proto.String(value)
						found = true
						break
					}
				}
				if !found {
					metric.Label = append(metric.Label, &dto.LabelPair{Name: proto.String(name), Value: proto.String(value)})
				}
			}
			sort.Slice(metric.Label, func(first, second int) bool {
				return metric.Label[first].GetName() < metric.Label[second].GetName()
			})
			if metric.TimestampMs == nil {
				metric.TimestampMs = &timestamp
			}
		}
		if _, err := expfmt.MetricFamilyToText(&data, family); err != nil {
			return nil, err
		}
	}
	return data.Bytes(), nil
}

func validatePushMetrics(endpoint string, interval, timeout time.Duration) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Fragment != "" {
		return fmt.Errorf("pushmetrics.url must be an HTTP(S) URL without a fragment")
	}
	if interval <= 0 || timeout <= 0 {
		return fmt.Errorf("pushmetrics.interval and pushmetrics.timeout must be greater than 0")
	}
	return nil
}

func runPushMetrics(ctx context.Context, gatherer prometheus.Gatherer, queue *metricsQueue, endpoint string, interval, timeout time.Duration, extraLabels prometheus.Labels) {
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()
	ready := make(chan struct{}, 1)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		retry := time.NewTicker(interval)
		defer retry.Stop()
		for {
			if err := queue.flush(ctx, client, endpoint); err != nil && ctx.Err() == nil {
				log.WithError(err).Warn("Metrics push failed; queued data will be retried")
			}
			select {
			case <-ctx.Done():
				return
			case <-ready:
			case <-retry.C:
			}
		}
	}()
	defer workers.Wait()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		data, err := captureMetrics(gatherer, time.Now(), extraLabels)
		if err == nil {
			err = queue.enqueue(data)
		}
		if err != nil {
			log.WithError(err).Error("Could not queue metrics snapshot")
		} else {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// pushMetricsWorker represents a single push metrics endpoint with its own queue
type pushMetricsWorker struct {
	endpoint string
	queue    *metricsQueue
	index    int // order-based index, not address-based
}

// runPushMetricsMulti manages multiple independent push metric workers,
// each with its own queue ordered by index rather than endpoint value
func runPushMetricsMulti(ctx context.Context, gatherer prometheus.Gatherer, workers []*pushMetricsWorker, interval, timeout time.Duration, extraLabels prometheus.Labels) {
	if len(workers) == 0 {
		return
	}

	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()

	// ready signal for each worker
	readyChans := make([]chan struct{}, len(workers))
	for i := range readyChans {
		readyChans[i] = make(chan struct{}, 1)
	}

	// Start a flush goroutine for each worker
	var wg sync.WaitGroup
	for idx, worker := range workers {
		wg.Add(1)
		go func(w *pushMetricsWorker, readyChan chan struct{}) {
			defer wg.Done()
			retry := time.NewTicker(interval)
			defer retry.Stop()
			for {
				if err := w.queue.flush(ctx, client, w.endpoint); err != nil && ctx.Err() == nil {
					log.WithError(err).Warnf("Metrics push to endpoint #%d (%s) failed; queued data will be retried", w.index+1, w.endpoint)
				}
				select {
				case <-ctx.Done():
					return
				case <-readyChan:
				case <-retry.C:
				}
			}
		}(worker, readyChans[idx])
	}

	// Capture and enqueue metrics to all workers
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		data, err := captureMetrics(gatherer, time.Now(), extraLabels)
		if err == nil {
			for idx, worker := range workers {
				if qErr := worker.queue.enqueue(data); qErr != nil {
					log.WithError(qErr).Errorf("Could not queue metrics snapshot for endpoint #%d", idx+1)
				} else {
					select {
					case readyChans[idx] <- struct{}{}:
					default:
					}
				}
			}
		} else {
			log.WithError(err).Error("Could not capture metrics snapshot")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
