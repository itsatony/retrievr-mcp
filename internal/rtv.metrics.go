package internal

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ---------------------------------------------------------------------------
// Metrics constants
// ---------------------------------------------------------------------------

const (
	metricsNamespace = "rtv"

	metricSearchTotal           = "search_total"
	metricSearchDurationSeconds = "search_duration_seconds"
	metricGetTotal              = "get_total"
	metricRateLimitWaitsTotal   = "rate_limit_waits_total"
	metricCacheHitsTotal        = "cache_hits_total"
	metricCacheMissesTotal      = "cache_misses_total"

	metricLabelSource = "source"
	metricLabelStatus = "status"

	metricStatusSuccess = "success"
	metricStatusError   = "error"

	metricsEndpointPath = "/metrics"

	metricHelpSearchTotal         = "Total number of search operations by source and status."
	metricHelpSearchDuration      = "Duration of search operations in seconds by source."
	metricHelpGetTotal            = "Total number of get operations by source and status."
	metricHelpRateLimitWaitsTotal = "Total number of rate limit waits by source."
	metricHelpCacheHitsTotal      = "Total number of cache hits."
	metricHelpCacheMissesTotal    = "Total number of cache misses."
)

// metricSearchDurationBuckets defines histogram bucket boundaries (in seconds)
// for search latency. Covers sub-second to 30s (the typical per-source timeout
// is 10s, but fan-out + merge can push wall-clock higher).
var metricSearchDurationBuckets = []float64{0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0}

// ---------------------------------------------------------------------------
// Metrics type
// ---------------------------------------------------------------------------

// Metrics holds all Prometheus metric collectors for the application.
// Uses a custom registry for testability (no global state pollution).
// All convenience methods are nil-receiver safe, so callers may pass a nil
// *Metrics when observability is disabled.
type Metrics struct {
	Registry *prometheus.Registry

	SearchTotal         *prometheus.CounterVec
	SearchDuration      *prometheus.HistogramVec
	GetTotal            *prometheus.CounterVec
	RateLimitWaitsTotal *prometheus.CounterVec
	CacheHitsTotal      prometheus.Counter
	CacheMissesTotal    prometheus.Counter
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// NewMetrics creates and registers all Prometheus metrics with a fresh,
// custom registry. The registry can be exposed via promhttp.HandlerFor().
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	searchTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      metricSearchTotal,
		Help:      metricHelpSearchTotal,
	}, []string{metricLabelSource, metricLabelStatus})

	searchDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricsNamespace,
		Name:      metricSearchDurationSeconds,
		Help:      metricHelpSearchDuration,
		Buckets:   metricSearchDurationBuckets,
	}, []string{metricLabelSource})

	getTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      metricGetTotal,
		Help:      metricHelpGetTotal,
	}, []string{metricLabelSource, metricLabelStatus})

	rateLimitWaitsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      metricRateLimitWaitsTotal,
		Help:      metricHelpRateLimitWaitsTotal,
	}, []string{metricLabelSource})

	cacheHitsTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      metricCacheHitsTotal,
		Help:      metricHelpCacheHitsTotal,
	})

	cacheMissesTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      metricCacheMissesTotal,
		Help:      metricHelpCacheMissesTotal,
	})

	reg.MustRegister(
		searchTotal,
		searchDuration,
		getTotal,
		rateLimitWaitsTotal,
		cacheHitsTotal,
		cacheMissesTotal,
	)

	return &Metrics{
		Registry:            reg,
		SearchTotal:         searchTotal,
		SearchDuration:      searchDuration,
		GetTotal:            getTotal,
		RateLimitWaitsTotal: rateLimitWaitsTotal,
		CacheHitsTotal:      cacheHitsTotal,
		CacheMissesTotal:    cacheMissesTotal,
	}
}

// ---------------------------------------------------------------------------
// Nil-safe convenience methods
// ---------------------------------------------------------------------------

// RecordSearch records a search operation's outcome and duration for a
// single source.
func (m *Metrics) RecordSearch(source, status string, duration time.Duration) {
	if m == nil {
		return
	}
	m.SearchTotal.WithLabelValues(source, status).Inc()
	m.SearchDuration.WithLabelValues(source).Observe(duration.Seconds())
}

// RecordGet records a get operation's outcome for a single source.
func (m *Metrics) RecordGet(source, status string) {
	if m == nil {
		return
	}
	m.GetTotal.WithLabelValues(source, status).Inc()
}

// RecordRateLimitWait records a rate limit wait event for a source.
func (m *Metrics) RecordRateLimitWait(source string) {
	if m == nil {
		return
	}
	m.RateLimitWaitsTotal.WithLabelValues(source).Inc()
}

// RecordCacheHit records a cache hit.
func (m *Metrics) RecordCacheHit() {
	if m == nil {
		return
	}
	m.CacheHitsTotal.Inc()
}

// RecordCacheMiss records a cache miss.
func (m *Metrics) RecordCacheMiss() {
	if m == nil {
		return
	}
	m.CacheMissesTotal.Inc()
}
