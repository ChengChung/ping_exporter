package main

import (
	"strings"
	"testing"
	"time"

	"github.com/czerwonk/ping_exporter/config"
	mon "github.com/digineo/go-ping/monitor"
	"github.com/prometheus/client_golang/prometheus"
)

func TestCollectorConfigLabels(t *testing.T) {
	cfg, err := config.FromYAML(strings.NewReader(`labels:
  location: CN
  job: ping
targets:
  - host: 104.245.12.188
    target: xTom.SJC
    location: US
    provider: xTom
  - host: 128.1.145.129
    target: Zenlayer.HKG
`))
	if err != nil {
		t.Fatal(err)
	}
	monitor := mon.New(nil, time.Second, time.Second)
	collector := NewPingCollector(true, rttInMills, monitor, cfg)
	collector.metrics = map[string]*mon.Metrics{
		"104.245.12.188 104.245.12.188 4": {PacketsSent: 1},
		"128.1.145.129 128.1.145.129 4":   {PacketsSent: 1},
	}
	registry := prometheus.NewRegistry()
	if err := registry.Register(collector); err != nil {
		t.Fatal(err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var foundUp, foundFirst, foundSecond bool
	for _, family := range families {
		for _, metric := range family.Metric {
			labels := make(map[string]string)
			for _, label := range metric.Label {
				if _, exists := labels[label.GetName()]; exists {
					t.Fatalf("duplicate label %q", label.GetName())
				}
				labels[label.GetName()] = label.GetValue()
			}
			if labels["job"] != "ping" {
				t.Fatalf("global label missing on %s", family.GetName())
			}
			switch labels["ip"] {
			case "104.245.12.188":
				foundFirst = true
				if labels["target"] != "xTom.SJC" || labels["location"] != "US" || labels["provider"] != "xTom" {
					t.Fatalf("host overrides not applied: %v", labels)
				}
			case "128.1.145.129":
				foundSecond = true
				if labels["target"] != "Zenlayer.HKG" || labels["location"] != "CN" || labels["provider"] != "" {
					t.Fatalf("host labels leaked: %v", labels)
				}
			default:
				if family.GetName() != "ping_up" || labels["location"] != "CN" || labels["provider"] != "" || labels["target"] != "" {
					t.Fatalf("unexpected global metric: %s %v", family.GetName(), labels)
				}
				foundUp = true
			}
		}
	}
	if !foundUp || !foundFirst || !foundSecond {
		t.Fatal("expected exporter and both host metrics")
	}
	updated, err := config.FromYAML(strings.NewReader("labels: {location: JP, instance: changed}\ntargets: []"))
	if err != nil {
		t.Fatal(err)
	}
	collector.UpdateConfig(updated)
	families, err = registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || families[0].GetName() != "ping_up" {
		t.Fatalf("removed targets still produce metrics: %v", families)
	}
	labels := make(map[string]string)
	for _, label := range families[0].Metric[0].Label {
		labels[label.GetName()] = label.GetValue()
	}
	if labels["location"] != "JP" || labels["instance"] != "changed" || labels["job"] != "" {
		t.Fatalf("global labels were not reloaded: %v", labels)
	}
	if cfg.Labels["location"] != "CN" || len(cfg.Targets) != 2 {
		t.Fatal("collector mutated the original configuration")
	}
}
