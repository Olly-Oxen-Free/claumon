package otel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func snapshot() Snapshot {
	return Snapshot{
		At: time.Unix(1_700_000_000, 0).UTC(),
		Metrics: []Metric{
			{Name: "claumon.usage.utilization", Unit: "%", Value: 44,
				Attributes: map[string]string{"window": "weekly"}},
			{Name: "gen_ai.client.token.usage", Unit: "1", Value: 1234, Sum: true,
				Attributes: map[string]string{"gen_ai.token.type": "output"}},
		},
	}
}

func TestDisabledExporterSendsNothing(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Endpoint = srv.URL
	if err := New(cfg).Export(context.Background(), snapshot()); err != nil {
		t.Fatalf("disabled export returned %v", err)
	}
	if hits != 0 {
		t.Fatalf("disabled exporter made %d requests", hits)
	}
}

func TestExportPostsToTheMetricsPath(t *testing.T) {
	var gotPath, gotType, gotAuth string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotType = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Enabled = true
	// A trailing slash must not produce a doubled path.
	cfg.Endpoint = srv.URL + "/"
	cfg.Headers = map[string]string{"Authorization": "Bearer t0ken"}

	if err := New(cfg).Export(context.Background(), snapshot()); err != nil {
		t.Fatalf("export: %v", err)
	}
	if gotPath != "/v1/metrics" {
		t.Fatalf("path = %q, want /v1/metrics", gotPath)
	}
	if gotType != "application/json" {
		t.Fatalf("content-type = %q", gotType)
	}
	if gotAuth != "Bearer t0ken" {
		t.Fatalf("configured header not sent, got %q", gotAuth)
	}
	if len(body) == 0 {
		t.Fatal("empty body")
	}
}

func TestCollectorErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = srv.URL
	if err := New(cfg).Export(context.Background(), snapshot()); err == nil {
		t.Fatal("a 500 from the collector must surface as an error")
	}
}

func TestEncodedPayloadMatchesTheOTLPShape(t *testing.T) {
	raw, err := json.Marshal(Encode("claumon", snapshot()))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	rms := got["resourceMetrics"].([]any)
	if len(rms) != 1 {
		t.Fatalf("resourceMetrics = %d, want 1", len(rms))
	}
	rm := rms[0].(map[string]any)

	res := rm["resource"].(map[string]any)["attributes"].([]any)
	svc := res[0].(map[string]any)
	if svc["key"] != "service.name" {
		t.Fatalf("resource attribute = %v", svc["key"])
	}
	if svc["value"].(map[string]any)["stringValue"] != "claumon" {
		t.Fatalf("service.name = %v", svc["value"])
	}

	ms := rm["scopeMetrics"].([]any)[0].(map[string]any)["metrics"].([]any)
	if len(ms) != 2 {
		t.Fatalf("metrics = %d, want 2", len(ms))
	}

	// A utilization reading is a gauge, not a counter.
	gauge := ms[0].(map[string]any)
	if _, ok := gauge["gauge"]; !ok {
		t.Fatalf("first metric is not a gauge: %v", gauge)
	}
	dp := gauge["gauge"].(map[string]any)["dataPoints"].([]any)[0].(map[string]any)
	if dp["timeUnixNano"] != "1700000000000000000" {
		t.Fatalf("timeUnixNano = %v", dp["timeUnixNano"])
	}
	if dp["asDouble"].(float64) != 44 {
		t.Fatalf("asDouble = %v", dp["asDouble"])
	}

	// Token usage is a cumulative monotonic sum.
	sum := ms[1].(map[string]any)["sum"].(map[string]any)
	if sum["aggregationTemporality"].(float64) != 2 {
		t.Fatalf("temporality = %v, want 2 (cumulative)", sum["aggregationTemporality"])
	}
	if sum["isMonotonic"] != true {
		t.Fatal("token counter must be monotonic")
	}
}

func TestAttributeOrderIsStable(t *testing.T) {
	m := Metric{Name: "x", Attributes: map[string]string{"z": "1", "a": "2", "m": "3"}}
	snap := Snapshot{At: time.Unix(1, 0), Metrics: []Metric{m}}
	first, _ := json.Marshal(Encode("claumon", snap))
	for i := 0; i < 20; i++ {
		next, _ := json.Marshal(Encode("claumon", snap))
		if string(first) != string(next) {
			t.Fatal("payload is not byte-stable across runs")
		}
	}
}

func TestIntervalFallsBackToTheDefault(t *testing.T) {
	if got := (Config{}).Interval(); got != 60*time.Second {
		t.Fatalf("interval = %v, want 60s", got)
	}
}
