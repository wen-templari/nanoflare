package runtime

import (
	"math"
	"sort"
	"sync"
	"time"
)

const (
	defaultDurationTelemetryWindow       = 24 * time.Hour
	defaultDurationTelemetryRecentWindow = 5 * time.Minute
	defaultDurationTelemetryBucketSize   = 5 * time.Minute
)

type DurationTraceEvent struct {
	ScriptName     string  `json:"scriptName"`
	EventTimestamp float64 `json:"eventTimestamp"`
	DurationMs     float64 `json:"durationMs"`
	Outcome        string  `json:"outcome,omitempty"`
}

type durationSample struct {
	timestamp  time.Time
	durationMs float64
	outcome    string
}

type DurationStats struct {
	Available           bool
	DurationMsAvg       float64
	DurationMsP95       float64
	DurationMsPerSecond float64
	DurationSeries      []float64
}

type DurationTelemetry struct {
	mu           sync.Mutex
	samples      map[string][]durationSample
	window       time.Duration
	recentWindow time.Duration
	bucketSize   time.Duration
	now          func() time.Time
}

func NewDurationTelemetry() *DurationTelemetry {
	return &DurationTelemetry{
		samples:      make(map[string][]durationSample),
		window:       defaultDurationTelemetryWindow,
		recentWindow: defaultDurationTelemetryRecentWindow,
		bucketSize:   defaultDurationTelemetryBucketSize,
		now:          time.Now,
	}
}

func (t *DurationTelemetry) RecordBatch(events []DurationTraceEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now().UTC()
	for _, event := range events {
		if event.ScriptName == "" || event.DurationMs < 0 {
			continue
		}
		timestamp := now
		if event.EventTimestamp > 0 {
			timestamp = time.UnixMilli(int64(event.EventTimestamp)).UTC()
		}
		t.samples[event.ScriptName] = append(t.samples[event.ScriptName], durationSample{
			timestamp:  timestamp,
			durationMs: event.DurationMs,
			outcome:    event.Outcome,
		})
	}
	t.pruneLocked(now)
}

func (t *DurationTelemetry) Stats(appID string) DurationStats {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now().UTC()
	t.pruneLocked(now)

	samples := t.samples[appID]
	if len(samples) == 0 {
		return DurationStats{}
	}
	return t.statsLocked(samples, now)
}

func (t *DurationTelemetry) statsLocked(samples []durationSample, now time.Time) DurationStats {
	recentCutoff := now.Add(-t.recentWindow)
	recent := make([]float64, 0, len(samples))
	recentTotal := 0.0
	seriesBuckets := int(math.Ceil(float64(t.window) / float64(t.bucketSize)))
	series := make([]float64, seriesBuckets)
	windowCutoff := now.Add(-t.window)

	for _, sample := range samples {
		if !sample.timestamp.Before(recentCutoff) {
			recent = append(recent, sample.durationMs)
			recentTotal += sample.durationMs
		}
		if sample.timestamp.Before(windowCutoff) || sample.timestamp.After(now) {
			continue
		}
		age := now.Sub(sample.timestamp)
		index := seriesBuckets - 1 - int(age/t.bucketSize)
		if index < 0 || index >= seriesBuckets {
			continue
		}
		series[index] += sample.durationMs / t.bucketSize.Seconds()
	}

	stats := DurationStats{
		Available:      len(recent) > 0,
		DurationSeries: trimLeadingZeros(series),
	}
	if len(recent) == 0 {
		return stats
	}

	stats.DurationMsAvg = recentTotal / float64(len(recent))
	stats.DurationMsPerSecond = recentTotal / t.recentWindow.Seconds()

	sort.Float64s(recent)
	index := int(math.Ceil(float64(len(recent))*0.95)) - 1
	if index < 0 {
		index = 0
	}
	stats.DurationMsP95 = recent[index]
	return stats
}

func (t *DurationTelemetry) pruneLocked(now time.Time) {
	cutoff := now.Add(-t.window)
	for appID, samples := range t.samples {
		index := 0
		for index < len(samples) && samples[index].timestamp.Before(cutoff) {
			index++
		}
		if index == len(samples) {
			delete(t.samples, appID)
			continue
		}
		if index > 0 {
			t.samples[appID] = append([]durationSample(nil), samples[index:]...)
		}
	}
}

func trimLeadingZeros(values []float64) []float64 {
	index := 0
	for index < len(values) && values[index] == 0 {
		index++
	}
	if index == len(values) {
		return []float64{}
	}
	return append([]float64(nil), values[index:]...)
}
