// Package otel pushes claumon's numbers to an OpenTelemetry collector.
//
// The point is to get claumon's data into Grafana without running the usual
// four-container observability stack alongside it. claumon already computes
// utilization, token counts, cost, and a forecast; this hands those to whatever
// collector you already run.
//
// The OTLP/HTTP JSON encoding is written out by hand rather than pulled in
// from the OpenTelemetry SDK. That SDK is a large dependency tree, and claumon
// ships as one static binary with no runtime requirements; the payload here is
// a few nested structs and is pinned by a golden test.
//
// Metric names follow the gen_ai.* semantic conventions where one fits
// (token usage), and use a claumon.* prefix where none does (utilization
// against a subscription's rate limits has no convention).
package otel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Config controls where metrics go.
type Config struct {
	// Enabled gates export. Off by default.
	Enabled bool `json:"enabled"`
	// Endpoint is the collector's OTLP/HTTP base URL, e.g.
	// http://localhost:4318. The /v1/metrics path is appended.
	Endpoint string `json:"endpoint"`
	// Headers are added to each request, for collectors behind auth.
	Headers map[string]string `json:"headers,omitempty"`
	// IntervalSecs is how often metrics are pushed. Defaults to 60.
	IntervalSecs int `json:"interval_seconds"`
	// ServiceName labels the resource. Defaults to "claumon".
	ServiceName string `json:"service_name"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:      false,
		Endpoint:     "http://localhost:4318",
		IntervalSecs: 60,
		ServiceName:  "claumon",
	}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.IntervalSecs <= 0 {
		c.IntervalSecs = d.IntervalSecs
	}
	if c.ServiceName == "" {
		c.ServiceName = d.ServiceName
	}
	if c.Endpoint == "" {
		c.Endpoint = d.Endpoint
	}
	return c
}

// Interval is the configured push period.
func (c Config) Interval() time.Duration {
	return time.Duration(c.withDefaults().IntervalSecs) * time.Second
}

// Metric is one measurement ready for export.
type Metric struct {
	Name string
	// Unit as a UCUM string, e.g. "%" or "1". Optional.
	Unit string
	// Description is optional help text.
	Description string
	Value       float64
	// Attributes are the metric's labels.
	Attributes map[string]string
	// Sum marks a monotonic cumulative counter. False makes it a gauge.
	Sum bool
}

// Snapshot is everything claumon knows at one instant, in metric form.
type Snapshot struct {
	At      time.Time
	Metrics []Metric
}

// Exporter posts snapshots to a collector.
type Exporter struct {
	cfg    Config
	client *http.Client
}

func New(cfg Config) *Exporter {
	return &Exporter{
		cfg:    cfg.withDefaults(),
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (e *Exporter) Enabled() bool { return e.cfg.Enabled }

// Config returns the exporter's settings.
func (e *Exporter) Config() Config { return e.cfg }

// Export sends one snapshot. Returns the collector's error, if any; the caller
// logs and carries on, because a missing collector must never stop the poller.
func (e *Exporter) Export(ctx context.Context, snap Snapshot) error {
	if !e.cfg.Enabled || len(snap.Metrics) == 0 {
		return nil
	}
	body, err := json.Marshal(Encode(e.cfg.ServiceName, snap))
	if err != nil {
		return fmt.Errorf("encoding metrics: %w", err)
	}

	url := strings.TrimSuffix(e.cfg.Endpoint, "/") + "/v1/metrics"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.cfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("posting to %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("collector returned %s", resp.Status)
	}
	return nil
}

// --- OTLP/HTTP JSON wire types -------------------------------------------
//
// A minimal subset of opentelemetry.proto.collector.metrics.v1: enough for
// gauges and cumulative sums with string attributes. Field names and casing
// are fixed by the protobuf JSON mapping and must not be "tidied".

type payload struct {
	ResourceMetrics []resourceMetrics `json:"resourceMetrics"`
}

type resourceMetrics struct {
	Resource     resource       `json:"resource"`
	ScopeMetrics []scopeMetrics `json:"scopeMetrics"`
}

type resource struct {
	Attributes []keyValue `json:"attributes"`
}

type scopeMetrics struct {
	Scope   scope        `json:"scope"`
	Metrics []otlpMetric `json:"metrics"`
}

type scope struct {
	Name string `json:"name"`
}

type otlpMetric struct {
	Name        string      `json:"name"`
	Unit        string      `json:"unit,omitempty"`
	Description string      `json:"description,omitempty"`
	Gauge       *dataSet    `json:"gauge,omitempty"`
	Sum         *sumDataSet `json:"sum,omitempty"`
}

type dataSet struct {
	DataPoints []numberDataPoint `json:"dataPoints"`
}

type sumDataSet struct {
	DataPoints []numberDataPoint `json:"dataPoints"`
	// 2 is AGGREGATION_TEMPORALITY_CUMULATIVE.
	AggregationTemporality int  `json:"aggregationTemporality"`
	IsMonotonic            bool `json:"isMonotonic"`
}

type numberDataPoint struct {
	Attributes   []keyValue `json:"attributes,omitempty"`
	TimeUnixNano string     `json:"timeUnixNano"`
	AsDouble     float64    `json:"asDouble"`
}

type keyValue struct {
	Key   string     `json:"key"`
	Value anyValueOf `json:"value"`
}

type anyValueOf struct {
	StringValue string `json:"stringValue"`
}

// Encode converts a snapshot to the OTLP JSON payload. Exported so the wire
// format can be asserted in tests without a collector.
func Encode(serviceName string, snap Snapshot) any {
	ts := fmt.Sprintf("%d", snap.At.UnixNano())
	metrics := make([]otlpMetric, 0, len(snap.Metrics))

	for _, m := range snap.Metrics {
		point := numberDataPoint{
			Attributes:   attrs(m.Attributes),
			TimeUnixNano: ts,
			AsDouble:     m.Value,
		}
		out := otlpMetric{Name: m.Name, Unit: m.Unit, Description: m.Description}
		if m.Sum {
			out.Sum = &sumDataSet{
				DataPoints:             []numberDataPoint{point},
				AggregationTemporality: 2,
				IsMonotonic:            true,
			}
		} else {
			out.Gauge = &dataSet{DataPoints: []numberDataPoint{point}}
		}
		metrics = append(metrics, out)
	}

	return payload{
		ResourceMetrics: []resourceMetrics{{
			Resource: resource{Attributes: []keyValue{{
				Key:   "service.name",
				Value: anyValueOf{StringValue: serviceName},
			}}},
			ScopeMetrics: []scopeMetrics{{
				Scope:   scope{Name: "claumon"},
				Metrics: metrics,
			}},
		}},
	}
}

// attrs converts a label map to OTLP key-values, sorted so a payload is
// byte-stable across runs (Go map order is not).
func attrs(m map[string]string) []keyValue {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	out := make([]keyValue, 0, len(keys))
	for _, k := range keys {
		out = append(out, keyValue{Key: k, Value: anyValueOf{StringValue: m[k]}})
	}
	return out
}

func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}
