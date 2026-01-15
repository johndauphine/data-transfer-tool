package monitor

import (
	"testing"
)

func TestAnalyzeTrends(t *testing.T) {
	t.Run("insufficient data", func(t *testing.T) {
		mc := &MetricsCollector{
			metrics: []PerformanceSnapshot{
				{Throughput: 100000},
				{Throughput: 100000},
			},
		}

		trends := mc.AnalyzeTrends()

		if !trends.Insufficient {
			t.Error("expected Insufficient=true with less than 3 samples")
		}
	})

	t.Run("throughput declining over 20%", func(t *testing.T) {
		mc := &MetricsCollector{
			metrics: []PerformanceSnapshot{
				{Throughput: 100000, CPUPercent: 50, MemoryPercent: 50},
				{Throughput: 85000, CPUPercent: 50, MemoryPercent: 50},
				{Throughput: 70000, CPUPercent: 50, MemoryPercent: 50}, // 30% decline from first
			},
		}

		trends := mc.AnalyzeTrends()

		if !trends.ThroughputDecreasing {
			t.Error("expected ThroughputDecreasing=true with >20% decline")
		}

		if trends.ThroughputDecline < 25 || trends.ThroughputDecline > 35 {
			t.Errorf("expected ThroughputDecline around 30%%, got %.1f%%", trends.ThroughputDecline)
		}
	})

	t.Run("throughput declining under 20%", func(t *testing.T) {
		mc := &MetricsCollector{
			metrics: []PerformanceSnapshot{
				{Throughput: 100000, CPUPercent: 50, MemoryPercent: 50},
				{Throughput: 95000, CPUPercent: 50, MemoryPercent: 50},
				{Throughput: 90000, CPUPercent: 50, MemoryPercent: 50}, // 10% decline
			},
		}

		trends := mc.AnalyzeTrends()

		if trends.ThroughputDecreasing {
			t.Error("expected ThroughputDecreasing=false with <20% decline")
		}

		if trends.ThroughputDecline < 8 || trends.ThroughputDecline > 12 {
			t.Errorf("expected ThroughputDecline around 10%%, got %.1f%%", trends.ThroughputDecline)
		}
	})

	t.Run("throughput increasing sets decline to zero", func(t *testing.T) {
		mc := &MetricsCollector{
			metrics: []PerformanceSnapshot{
				{Throughput: 100000, CPUPercent: 50, MemoryPercent: 50},
				{Throughput: 110000, CPUPercent: 50, MemoryPercent: 50},
				{Throughput: 120000, CPUPercent: 50, MemoryPercent: 50}, // Increasing
			},
		}

		trends := mc.AnalyzeTrends()

		if trends.ThroughputDecreasing {
			t.Error("expected ThroughputDecreasing=false when throughput increasing")
		}

		if trends.ThroughputDecline != 0 {
			t.Errorf("expected ThroughputDecline=0 when increasing, got %.1f%%", trends.ThroughputDecline)
		}
	})

	t.Run("stable throughput", func(t *testing.T) {
		mc := &MetricsCollector{
			metrics: []PerformanceSnapshot{
				{Throughput: 100000, CPUPercent: 50, MemoryPercent: 50},
				{Throughput: 100000, CPUPercent: 50, MemoryPercent: 50},
				{Throughput: 100000, CPUPercent: 50, MemoryPercent: 50},
			},
		}

		trends := mc.AnalyzeTrends()

		if trends.ThroughputDecreasing {
			t.Error("expected ThroughputDecreasing=false when stable")
		}

		if trends.ThroughputDecline != 0 {
			t.Errorf("expected ThroughputDecline=0 when stable, got %.1f%%", trends.ThroughputDecline)
		}
	})

	t.Run("CPU saturated", func(t *testing.T) {
		mc := &MetricsCollector{
			metrics: []PerformanceSnapshot{
				{Throughput: 100000, CPUPercent: 85, MemoryPercent: 50},
				{Throughput: 100000, CPUPercent: 90, MemoryPercent: 50},
				{Throughput: 100000, CPUPercent: 95, MemoryPercent: 50}, // >90%
			},
		}

		trends := mc.AnalyzeTrends()

		if !trends.CPUSaturated {
			t.Error("expected CPUSaturated=true when CPU >90%")
		}
	})

	t.Run("memory saturated", func(t *testing.T) {
		mc := &MetricsCollector{
			metrics: []PerformanceSnapshot{
				{Throughput: 100000, CPUPercent: 50, MemoryPercent: 80},
				{Throughput: 100000, CPUPercent: 50, MemoryPercent: 85},
				{Throughput: 100000, CPUPercent: 50, MemoryPercent: 90}, // >85%
			},
		}

		trends := mc.AnalyzeTrends()

		if !trends.MemorySaturated {
			t.Error("expected MemorySaturated=true when memory >85%")
		}
	})

	t.Run("memory increasing", func(t *testing.T) {
		mc := &MetricsCollector{
			metrics: []PerformanceSnapshot{
				{Throughput: 100000, CPUPercent: 50, MemoryPercent: 50, MemoryUsedMB: 1000},
				{Throughput: 100000, CPUPercent: 50, MemoryPercent: 56, MemoryUsedMB: 1100}, // >5% increase
				{Throughput: 100000, CPUPercent: 50, MemoryPercent: 63, MemoryUsedMB: 1210}, // >5% increase
			},
		}

		trends := mc.AnalyzeTrends()

		if !trends.MemoryIncreasing {
			t.Error("expected MemoryIncreasing=true when memory increases >5% per sample")
		}
	})
}

func TestGetRecentMetrics(t *testing.T) {
	mc := &MetricsCollector{
		metrics: []PerformanceSnapshot{
			{Throughput: 100000},
			{Throughput: 110000},
			{Throughput: 120000},
			{Throughput: 130000},
			{Throughput: 140000},
		},
	}

	t.Run("get last 3", func(t *testing.T) {
		recent := mc.GetRecentMetrics(3)

		if len(recent) != 3 {
			t.Errorf("expected 3 metrics, got %d", len(recent))
		}

		if recent[0].Throughput != 120000 {
			t.Errorf("expected first metric throughput 120000, got %.0f", recent[0].Throughput)
		}
	})

	t.Run("request more than available", func(t *testing.T) {
		recent := mc.GetRecentMetrics(10)

		if len(recent) != 5 {
			t.Errorf("expected 5 metrics (all available), got %d", len(recent))
		}
	})

	t.Run("empty metrics", func(t *testing.T) {
		emptyMc := &MetricsCollector{metrics: []PerformanceSnapshot{}}

		recent := emptyMc.GetRecentMetrics(3)

		if len(recent) != 0 {
			t.Errorf("expected 0 metrics, got %d", len(recent))
		}
	})
}
