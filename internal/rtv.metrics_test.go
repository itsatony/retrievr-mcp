package internal

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test constants
// ---------------------------------------------------------------------------

const (
	testMetricSource   = SourceArXiv
	testMetricSource2  = SourceS2
	testMetricDuration = 250 * time.Millisecond
)

// ---------------------------------------------------------------------------
// TestNewMetrics
// ---------------------------------------------------------------------------

func TestNewMetrics(t *testing.T) {
	t.Parallel()

	m := NewMetrics()
	require.NotNil(t, m)
	require.NotNil(t, m.Registry)
	require.NotNil(t, m.SearchTotal)
	require.NotNil(t, m.SearchDuration)
	require.NotNil(t, m.GetTotal)
	require.NotNil(t, m.RateLimitWaitsTotal)
	require.NotNil(t, m.CacheHitsTotal)
	require.NotNil(t, m.CacheMissesTotal)

	// Initialize all counter vecs with a dummy label to make them gatherable,
	// then verify all 6 metric families are registered.
	m.RecordSearch(testMetricSource, metricStatusSuccess, time.Millisecond)
	m.RecordGet(testMetricSource, metricStatusSuccess)
	m.RecordRateLimitWait(testMetricSource)
	m.RecordCacheHit()
	m.RecordCacheMiss()
	families, err := m.Registry.Gather()
	require.NoError(t, err)
	assert.Len(t, families, 6)
}

// ---------------------------------------------------------------------------
// TestRecordSearch
// ---------------------------------------------------------------------------

func TestRecordSearch(t *testing.T) {
	t.Parallel()

	m := NewMetrics()

	// Record success and error for different sources.
	m.RecordSearch(testMetricSource, metricStatusSuccess, testMetricDuration)
	m.RecordSearch(testMetricSource, metricStatusSuccess, testMetricDuration)
	m.RecordSearch(testMetricSource, metricStatusError, testMetricDuration)
	m.RecordSearch(testMetricSource2, metricStatusSuccess, testMetricDuration)

	// Counter assertions.
	successCount := testutil.ToFloat64(m.SearchTotal.WithLabelValues(testMetricSource, metricStatusSuccess))
	assert.Equal(t, float64(2), successCount)

	errorCount := testutil.ToFloat64(m.SearchTotal.WithLabelValues(testMetricSource, metricStatusError))
	assert.Equal(t, float64(1), errorCount)

	s2Count := testutil.ToFloat64(m.SearchTotal.WithLabelValues(testMetricSource2, metricStatusSuccess))
	assert.Equal(t, float64(1), s2Count)

	// Histogram should have observations. Gather and check sample count.
	families, err := m.Registry.Gather()
	require.NoError(t, err)
	var histSampleCount uint64
	for _, f := range families {
		if f.GetName() == metricsNamespace+"_"+metricSearchDurationSeconds {
			for _, metric := range f.GetMetric() {
				histSampleCount += metric.GetHistogram().GetSampleCount()
			}
		}
	}
	assert.Greater(t, histSampleCount, uint64(0))
}

// ---------------------------------------------------------------------------
// TestRecordGet
// ---------------------------------------------------------------------------

func TestRecordGet(t *testing.T) {
	t.Parallel()

	m := NewMetrics()

	m.RecordGet(testMetricSource, metricStatusSuccess)
	m.RecordGet(testMetricSource, metricStatusError)
	m.RecordGet(testMetricSource, metricStatusSuccess)

	successCount := testutil.ToFloat64(m.GetTotal.WithLabelValues(testMetricSource, metricStatusSuccess))
	assert.Equal(t, float64(2), successCount)

	errorCount := testutil.ToFloat64(m.GetTotal.WithLabelValues(testMetricSource, metricStatusError))
	assert.Equal(t, float64(1), errorCount)
}

// ---------------------------------------------------------------------------
// TestRecordRateLimitWait
// ---------------------------------------------------------------------------

func TestRecordRateLimitWait(t *testing.T) {
	t.Parallel()

	m := NewMetrics()

	m.RecordRateLimitWait(testMetricSource)
	m.RecordRateLimitWait(testMetricSource)
	m.RecordRateLimitWait(testMetricSource2)

	count := testutil.ToFloat64(m.RateLimitWaitsTotal.WithLabelValues(testMetricSource))
	assert.Equal(t, float64(2), count)

	count2 := testutil.ToFloat64(m.RateLimitWaitsTotal.WithLabelValues(testMetricSource2))
	assert.Equal(t, float64(1), count2)
}

// ---------------------------------------------------------------------------
// TestRecordCacheHitMiss
// ---------------------------------------------------------------------------

func TestRecordCacheHitMiss(t *testing.T) {
	t.Parallel()

	m := NewMetrics()

	m.RecordCacheHit()
	m.RecordCacheHit()
	m.RecordCacheHit()
	m.RecordCacheMiss()

	hits := testutil.ToFloat64(m.CacheHitsTotal)
	assert.Equal(t, float64(3), hits)

	misses := testutil.ToFloat64(m.CacheMissesTotal)
	assert.Equal(t, float64(1), misses)
}

// ---------------------------------------------------------------------------
// TestMetricsNilSafety
// ---------------------------------------------------------------------------

func TestMetricsNilSafety(t *testing.T) {
	t.Parallel()

	// All methods on a nil *Metrics must not panic.
	var m *Metrics

	assert.NotPanics(t, func() {
		m.RecordSearch(testMetricSource, metricStatusSuccess, testMetricDuration)
	})
	assert.NotPanics(t, func() {
		m.RecordGet(testMetricSource, metricStatusSuccess)
	})
	assert.NotPanics(t, func() {
		m.RecordRateLimitWait(testMetricSource)
	})
	assert.NotPanics(t, func() {
		m.RecordCacheHit()
	})
	assert.NotPanics(t, func() {
		m.RecordCacheMiss()
	})
}
