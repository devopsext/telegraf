package prometheus

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/influxdata/telegraf/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleTextFormat = `# HELP go_gc_duration_seconds A summary of the GC invocation durations.
# TYPE go_gc_duration_seconds summary
go_gc_duration_seconds{quantile="0"} 0.00010425500000000001
go_gc_duration_seconds{quantile="0.25"} 0.000139108
go_gc_duration_seconds{quantile="0.5"} 0.00015749400000000002
go_gc_duration_seconds{quantile="0.75"} 0.000331463
go_gc_duration_seconds{quantile="1"} 0.000667154
go_gc_duration_seconds_sum 0.0018183950000000002
go_gc_duration_seconds_count 7
# HELP go_goroutines Number of goroutines that currently exist.
# TYPE go_goroutines gauge
go_goroutines 15
# HELP test_metric An untyped metric with a timestamp
# TYPE test_metric untyped
test_metric{label="value"} 1.0 1490802350000
`
const sampleSummaryTextFormat = `# HELP go_gc_duration_seconds A summary of the GC invocation durations.
# TYPE go_gc_duration_seconds summary
go_gc_duration_seconds{quantile="0"} 0.00010425500000000001
go_gc_duration_seconds{quantile="0.25"} 0.000139108
go_gc_duration_seconds{quantile="0.5"} 0.00015749400000000002
go_gc_duration_seconds{quantile="0.75"} 0.000331463
go_gc_duration_seconds{quantile="1"} 0.000667154
go_gc_duration_seconds_sum 0.0018183950000000002
go_gc_duration_seconds_count 7
`
const sampleGaugeTextFormat = `
# HELP go_goroutines Number of goroutines that currently exist.
# TYPE go_goroutines gauge
go_goroutines 15 1490802350000
`

const corruptedTextFormat = `
# HELP nginx_vts_info Nginx info
# TYPE nginx_vts_info gauge
# HELP nginx_vts_start_time_seconds Nginx start time
# TYPE nginx_vts_start_time_seconds gauge
# HELP nginx_vts_main_connections Nginx connections
# TYPE nginx_vts_main_connections gauge
# HELP nginx_vts_main_shm_usage_bytes Shared memory [vhost_traffic_status] info
# TYPE nginx_vts_main_shm_usage_bytes gauge
# HELP nginx_vts_server_bytes_total The request/response bytes
# TYPE nginx_vts_server_bytes_total counter
# HELP nginx_vts_server_requests_total The requests counter
# TYPE nginx_vts_server_requests_total counter
# HELP nginx_vts_server_request_seconds_total The request processing time in seconds
# TYPE nginx_vts_server_request_seconds_total counter
# HELP nginx_vts_server_request_seconds The average of request processing times in seconds
# TYPE nginx_vts_server_request_seconds gauge
# HELP nginx_vts_server_request_duration_seconds The histogram of request processing time
# TYPE nginx_vts_server_request_duration_seconds histogram
# HELP nginx_vts_server_cache_total The requests cache counter
# TYPE nginx_vts_server_cache_total counter
# HELP nginx_vts_filter_bytes_total The request/response bytes
# TYPE nginx_vts_filter_bytes_total counter
# HELP nginx_vts_filter_requests_total The requests counter
# TYPE nginx_vts_filter_requests_total counter
# HELP nginx_vts_filter_request_seconds_total The request processing time in seconds counter
# TYPE nginx_vts_filter_request_seconds_total counter
# HELP nginx_vts_filter_request_seconds The average of request processing times in seconds
# TYPE nginx_vts_filter_request_seconds gauge
# HELP nginx_vts_filter_request_duration_seconds The histogram of request processing time
# TYPE nginx_vts_filter_request_duration_seconds histogram
# HELP nginx_vts_filter_cache_total The requests cache counter
# TYPE nginx_vts_filter_cache_total counter
# HELP nginx_vts_upstream_bytes_total The request/response bytes
# TYPE nginx_vts_upstream_bytes_total counter
# HELP nginx_vts_upstream_requests_total The upstream requests counter
# TYPE nginx_vts_upstream_requests_total counter
# HELP nginx_vts_upstream_request_seconds_total The request Processing time including upstream in seconds
# TYPE nginx_vts_upstream_request_seconds_total counter
# HELP nginx_vts_upstream_request_seconds The average of request processing times including upstream in seconds
# TYPE nginx_vts_upstream_request_seconds gauge
# HELP nginx_vts_upstream_response_seconds_total The only upstream response processing time in seconds
# TYPE nginx_vts_upstream_response_seconds_total counter
# HELP nginx_vts_upstream_response_seconds The average of only upstream response processing times in seconds
# TYPE nginx_vts_upstream_response_seconds gauge
# HELP nginx_vts_upstream_request_duration_seconds The histogram of request processing time including upstream
# TYPE nginx_vts_upstream_request_duration_seconds histogram
# HELP nginx_vts_upstream_response_duration_seconds The histogram of only upstream response processing time
# TYPE nginx_vts_upstream_response_duration_seconds histogram
# HELP nginx_vts_cache_usage_bytes THe cache zones info
# TYPE nginx_vts_cache_usage_bytes gauge
# HELP nginx_vts_cache_bytes_total The cache zones request/response bytes
# TYPE nginx_vts_cache_bytes_total counter
# HELP nginx_vts_cache_requests_total The cache requests counter
# TYPE nginx_vts_cache_requests_total counter
nginx_vts_filter_cache_total{filter="path::302",filter_name="/metatrader_4/",status="scarce"} 0
nginx_vts_filter_bytes_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css",direction="in"} 3407
nginx_vts_filter_bytes_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css",direction="out"} 2803
nginx_vts_filter_requests_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css",direction="1xx"} 0
nginx_vts_filter_requests_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css",direction="2xx"} 0
nginx_vts_filter_requests_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css",direction="3xx"} 0
nginx_vts_filter_requests_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css",direction="4xx"} 0
nginx_vts_filter_requests_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css",direction="5xx"} 3
nginx_vts_filter_requests_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css",direction="total"} 3
nginx_vts_filter_request_seconds_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css"} 1.250
nginx_vts_filter_request_seconds{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css"} 0.000
nginx_vts_filter_cache_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css",status="miss"} 2
nginx_vts_filter_cache_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css",status="bypass"} 0
nginx_vts_filter_cache_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css",status="expired"} 0
nginx_vts_filter_cache_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css",status="stale"} 0
nginx_vts_filter_cache_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css",status="updating"} 0
nginx_vts_filter_cache_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css",status="revalidated"} 0
nginx_vts_filter_cache_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css",status="hit"} 1
nginx_vts_filter_cache_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css",status="scarce"} 0
nginx_vts_filter_bytes_total{filter="path::302",filter_name="/intl/th/member/deposit",direction="in"} 9197
`

const corruptedTextFormat2 = `
# HELP nginx_vts_filter_bytes_total The request/response bytes
# TYPE nginx_vts_filter_bytes_total counter
nginx_vts_filter_bytes_total{filter="path::503",filter_name="/bin/querybuilder.json.servlet;
a.css",direction="in"} 3407
`

func TestPrometheusGeneratesMetrics(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, sampleTextFormat)
	}))
	defer ts.Close()

	p := &Prometheus{
		Log:    testutil.Logger{},
		URLs:   []string{ts.URL},
		URLTag: "url",
	}

	var acc testutil.Accumulator

	err := acc.GatherError(p.Gather)
	require.NoError(t, err)

	assert.True(t, acc.HasFloatField("go_gc_duration_seconds", "count"))
	assert.True(t, acc.HasFloatField("go_goroutines", "gauge"))
	assert.True(t, acc.HasFloatField("test_metric", "value"))
	assert.True(t, acc.HasTimestamp("test_metric", time.Unix(1490802350, 0)))
	assert.False(t, acc.HasTag("test_metric", "address"))
	assert.True(t, acc.TagValue("test_metric", "url") == ts.URL+"/metrics")
}

func TestPrometheusGeneratesMetricsWithHostNameTag(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, sampleTextFormat)
	}))
	defer ts.Close()

	p := &Prometheus{
		Log:                testutil.Logger{},
		KubernetesServices: []string{ts.URL},
		URLTag:             "url",
	}
	u, _ := url.Parse(ts.URL)
	tsAddress := u.Hostname()

	var acc testutil.Accumulator

	err := acc.GatherError(p.Gather)
	require.NoError(t, err)

	assert.True(t, acc.HasFloatField("go_gc_duration_seconds", "count"))
	assert.True(t, acc.HasFloatField("go_goroutines", "gauge"))
	assert.True(t, acc.HasFloatField("test_metric", "value"))
	assert.True(t, acc.HasTimestamp("test_metric", time.Unix(1490802350, 0)))
	assert.True(t, acc.TagValue("test_metric", "address") == tsAddress)
	assert.True(t, acc.TagValue("test_metric", "url") == ts.URL)
}

func TestPrometheusGeneratesMetricsAlthoughFirstDNSFails(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, sampleTextFormat)
	}))
	defer ts.Close()

	p := &Prometheus{
		Log:                testutil.Logger{},
		URLs:               []string{ts.URL},
		KubernetesServices: []string{"http://random.telegraf.local:88/metrics"},
	}

	var acc testutil.Accumulator

	err := acc.GatherError(p.Gather)
	require.NoError(t, err)

	assert.True(t, acc.HasFloatField("go_gc_duration_seconds", "count"))
	assert.True(t, acc.HasFloatField("go_goroutines", "gauge"))
	assert.True(t, acc.HasFloatField("test_metric", "value"))
	assert.True(t, acc.HasTimestamp("test_metric", time.Unix(1490802350, 0)))
}

func TestPrometheusGeneratesSummaryMetricsV2(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, sampleSummaryTextFormat)
	}))
	defer ts.Close()

	p := &Prometheus{
		URLs:          []string{ts.URL},
		URLTag:        "url",
		MetricVersion: 2,
	}

	var acc testutil.Accumulator

	err := acc.GatherError(p.Gather)
	require.NoError(t, err)

	assert.True(t, acc.TagSetValue("prometheus", "quantile") == "0")
	assert.True(t, acc.HasFloatField("prometheus", "go_gc_duration_seconds_sum"))
	assert.True(t, acc.HasFloatField("prometheus", "go_gc_duration_seconds_count"))
	assert.True(t, acc.TagValue("prometheus", "url") == ts.URL+"/metrics")

}

func TestPrometheusGeneratesGaugeMetricsV2(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, sampleGaugeTextFormat)
	}))
	defer ts.Close()

	p := &Prometheus{
		URLs:          []string{ts.URL},
		URLTag:        "url",
		MetricVersion: 2,
	}

	var acc testutil.Accumulator

	err := acc.GatherError(p.Gather)
	require.NoError(t, err)

	assert.True(t, acc.HasFloatField("prometheus", "go_goroutines"))
	assert.True(t, acc.TagValue("prometheus", "url") == ts.URL+"/metrics")
	assert.True(t, acc.HasTimestamp("prometheus", time.Unix(1490802350, 0)))
}

func TestPrometheusGeneratesMetricsRegexFilter(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, sampleTextFormat)
	}))
	defer ts.Close()

	p := &Prometheus{
		URLs:          []string{ts.URL},
		FilterMetrics: []string{"^go_gc_duration_seconds", "test_metric"}}

	var acc testutil.Accumulator

	p.Start(&acc)
	require.NoError(t, acc.GatherError(p.Gather))
	require.False(t, acc.HasMeasurement("go_gc_duration_seconds"))
	require.False(t, acc.HasMeasurement("test_metric"))

	pNoFilter := &Prometheus{
		URLs: []string{ts.URL}}

	var accNoFilter testutil.Accumulator

	pNoFilter.Start(&accNoFilter)
	require.NoError(t, accNoFilter.GatherError(pNoFilter.Gather))
	require.True(t, accNoFilter.HasMeasurement("go_gc_duration_seconds"))
	require.True(t, accNoFilter.HasMeasurement("test_metric"))

}

func TestPrometheusGeneratesMetricsAgainstCorruptedInput(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, corruptedTextFormat2)
	}))
	defer ts.Close()

	p := &Prometheus{
		URLs:          []string{ts.URL},
		MetricVersion: 2,
		FilterMetrics: []string{"::2.{2}", "^nginx_vts_filter_cache_total.*", "::3.{2}"}}

	var acc testutil.Accumulator

	p.Start(&acc)
	require.NoError(t, acc.GatherError(p.Gather))
	require.False(t, acc.HasMeasurement("nginx_vts_filter_cache_total"))
}
