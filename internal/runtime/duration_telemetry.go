package runtime

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	defaultDurationTelemetryWindow       = 24 * time.Hour
	defaultDurationTelemetryRecentWindow = 24 * time.Hour
	defaultDurationTelemetryBucketSize   = 5 * time.Minute
	defaultDurationTelemetryPersistEvery = 5 * time.Second
	durationTelemetryDiskVersion         = 2
	durationTelemetrySamplesPerBucket    = 32
)

type DurationTraceEvent struct {
	ScriptName     string  `json:"scriptName"`
	EventTimestamp float64 `json:"eventTimestamp"`
	DurationMs     float64 `json:"durationMs"`
	Outcome        string  `json:"outcome,omitempty"`
}

// durationSample is retained only to stream files written by the original
// per-request persistence format during an upgrade.
type durationSample struct {
	Timestamp  time.Time `json:"timestamp"`
	DurationMs float64   `json:"duration_ms"`
	Outcome    string    `json:"outcome,omitempty"`
}

type durationBucket struct {
	// Count and TotalDuration remain exact. Samples is a bounded reservoir used
	// only for percentile estimates, so request volume cannot grow persistence.
	StartUnixMilli int64     `json:"start_ms"`
	Count          uint64    `json:"count"`
	TotalDuration  float64   `json:"total_duration_ms"`
	Samples        []float64 `json:"samples,omitempty"`
}

type durationTelemetryDisk struct {
	Version int                         `json:"version"`
	Workers map[string][]durationBucket `json:"workers"`
}

type weightedDuration struct {
	value  float64
	weight float64
}

type DurationStats struct {
	Available           bool
	DurationMsAvg       float64
	DurationMsP95       float64
	DurationMsP90       float64
	DurationMsPerSecond float64
	DurationSeries      []float64
}

type DurationTelemetry struct {
	mu           sync.Mutex
	workers      map[string][]durationBucket
	window       time.Duration
	recentWindow time.Duration
	bucketSize   time.Duration
	now          func() time.Time
	persistPath  string
	persistEvery time.Duration
	lastPersist  time.Time
	persisting   bool
}

func NewDurationTelemetry() *DurationTelemetry {
	return &DurationTelemetry{
		workers:      make(map[string][]durationBucket),
		window:       defaultDurationTelemetryWindow,
		recentWindow: defaultDurationTelemetryRecentWindow,
		bucketSize:   defaultDurationTelemetryBucketSize,
		now:          time.Now,
		persistEvery: defaultDurationTelemetryPersistEvery,
	}
}

func NewPersistentDurationTelemetry(path string) (*DurationTelemetry, error) {
	telemetry := NewDurationTelemetry()
	telemetry.persistPath = path
	if err := telemetry.load(); err != nil {
		return nil, err
	}
	return telemetry, nil
}

func (t *DurationTelemetry) RecordBatch(events []DurationTraceEvent) {
	t.mu.Lock()
	now := t.now().UTC()
	for _, event := range events {
		if event.ScriptName == "" || event.DurationMs < 0 {
			continue
		}
		timestamp := now
		if event.EventTimestamp > 0 {
			timestamp = time.UnixMilli(int64(event.EventTimestamp)).UTC()
		}
		t.recordLocked(event.ScriptName, timestamp, event.DurationMs, now)
	}
	var snapshot durationTelemetryDisk
	if t.persistPath != "" && !t.persisting && (t.lastPersist.IsZero() || now.Sub(t.lastPersist) >= t.persistEvery) {
		t.persisting = true
		t.lastPersist = now
		snapshot = t.snapshotLocked()
	}
	t.mu.Unlock()

	if snapshot.Workers == nil {
		return
	}
	err := t.persistSnapshot(snapshot)
	t.mu.Lock()
	t.persisting = false
	if err != nil {
		// Telemetry should never break worker requests; retry on the next batch.
		t.lastPersist = time.Time{}
	}
	t.mu.Unlock()
}

func (t *DurationTelemetry) recordLocked(appID string, timestamp time.Time, durationMs float64, now time.Time) {
	if timestamp.Before(now.Add(-t.window)) || timestamp.After(now) {
		return
	}
	start := timestamp.Truncate(t.bucketSize).UnixMilli()
	buckets := t.workers[appID]
	index := sort.Search(len(buckets), func(i int) bool {
		return buckets[i].StartUnixMilli >= start
	})
	if index == len(buckets) || buckets[index].StartUnixMilli != start {
		buckets = append(buckets, durationBucket{})
		copy(buckets[index+1:], buckets[index:])
		buckets[index] = durationBucket{StartUnixMilli: start}
	}
	bucket := &buckets[index]
	bucket.Count++
	bucket.TotalDuration += durationMs
	if len(bucket.Samples) < durationTelemetrySamplesPerBucket {
		bucket.Samples = append(bucket.Samples, durationMs)
	} else {
		candidate := splitmix64(bucket.Count+uint64(bucket.StartUnixMilli)) % bucket.Count
		if candidate < uint64(len(bucket.Samples)) {
			bucket.Samples[candidate] = durationMs
		}
	}
	t.workers[appID] = buckets
	if bucket.Count == 1 {
		t.pruneWorkerLocked(appID, now)
	}
}

func splitmix64(value uint64) uint64 {
	value += 0x9e3779b97f4a7c15
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	return value ^ (value >> 31)
}

func (t *DurationTelemetry) Stats(appID string) DurationStats {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now().UTC()
	t.pruneWorkerLocked(appID, now)
	buckets := t.workers[appID]
	if len(buckets) == 0 {
		return DurationStats{}
	}
	return t.statsLocked(buckets, now)
}

func (t *DurationTelemetry) statsLocked(buckets []durationBucket, now time.Time) DurationStats {
	recentCutoff := now.Add(-t.recentWindow)
	recentTotal := 0.0
	recentCount := uint64(0)
	weighted := make([]weightedDuration, 0, len(buckets)*durationTelemetrySamplesPerBucket)
	seriesBuckets := int(math.Ceil(float64(t.window) / float64(t.bucketSize)))
	series := make([]float64, seriesBuckets)
	windowCutoff := now.Add(-t.window)

	for _, bucket := range buckets {
		start := time.UnixMilli(bucket.StartUnixMilli).UTC()
		end := start.Add(t.bucketSize)
		if end.After(recentCutoff) && !start.After(now) {
			recentTotal += bucket.TotalDuration
			recentCount += bucket.Count
			if len(bucket.Samples) > 0 {
				weight := float64(bucket.Count) / float64(len(bucket.Samples))
				for _, sample := range bucket.Samples {
					weighted = append(weighted, weightedDuration{value: sample, weight: weight})
				}
			}
		}
		if !end.After(windowCutoff) || start.After(now) {
			continue
		}
		age := now.Sub(end)
		if age < 0 {
			age = 0
		}
		index := seriesBuckets - 1 - int(age/t.bucketSize)
		if index >= 0 && index < seriesBuckets {
			series[index] += bucket.TotalDuration / t.bucketSize.Seconds()
		}
	}

	stats := DurationStats{
		Available:      recentCount > 0,
		DurationSeries: trimLeadingZeros(series),
	}
	if recentCount == 0 {
		return stats
	}

	stats.DurationMsAvg = recentTotal / float64(recentCount)
	stats.DurationMsPerSecond = recentTotal / t.recentWindow.Seconds()
	sort.Slice(weighted, func(i, j int) bool { return weighted[i].value < weighted[j].value })
	stats.DurationMsP90 = weightedPercentile(weighted, float64(recentCount), 0.90)
	stats.DurationMsP95 = weightedPercentile(weighted, float64(recentCount), 0.95)
	return stats
}

func weightedPercentile(values []weightedDuration, totalWeight, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	target := math.Ceil(totalWeight * quantile)
	cumulative := 0.0
	for _, value := range values {
		cumulative += value.weight
		if cumulative >= target {
			return value.value
		}
	}
	return values[len(values)-1].value
}

func (t *DurationTelemetry) pruneWorkerLocked(appID string, now time.Time) {
	buckets := t.workers[appID]
	cutoff := now.Add(-t.window)
	index := 0
	for index < len(buckets) {
		end := time.UnixMilli(buckets[index].StartUnixMilli).UTC().Add(t.bucketSize)
		if end.After(cutoff) {
			break
		}
		index++
	}
	if index == len(buckets) {
		delete(t.workers, appID)
		return
	}
	if index > 0 {
		t.workers[appID] = append([]durationBucket(nil), buckets[index:]...)
	}
}

func (t *DurationTelemetry) load() error {
	if t.persistPath == "" {
		return nil
	}
	file, err := os.Open(t.persistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	decoder := json.NewDecoder(bufio.NewReader(file))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return fmt.Errorf("duration telemetry: expected JSON object")
	}
	if !decoder.More() {
		_, err = decoder.Token()
		return err
	}
	keyToken, err := decoder.Token()
	if err != nil {
		return err
	}
	key, ok := keyToken.(string)
	if !ok {
		return fmt.Errorf("duration telemetry: expected object key")
	}
	if key == "version" {
		return t.loadCurrent(decoder)
	}
	return t.loadLegacy(decoder, key)
}

func (t *DurationTelemetry) loadCurrent(decoder *json.Decoder) error {
	var version int
	if err := decoder.Decode(&version); err != nil {
		return err
	}
	if version != durationTelemetryDiskVersion {
		return fmt.Errorf("duration telemetry: unsupported version %d", version)
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("duration telemetry: expected object key")
		}
		if key == "workers" {
			if err := decoder.Decode(&t.workers); err != nil {
				return err
			}
			continue
		}
		var ignored json.RawMessage
		if err := decoder.Decode(&ignored); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}

func (t *DurationTelemetry) loadLegacy(decoder *json.Decoder, firstAppID string) error {
	now := t.now().UTC()
	appID := firstAppID
	for {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
			return fmt.Errorf("duration telemetry: expected sample array for %q", appID)
		}
		for decoder.More() {
			var sample durationSample
			if err := decoder.Decode(&sample); err != nil {
				return err
			}
			if sample.DurationMs >= 0 {
				t.recordLocked(appID, sample.Timestamp.UTC(), sample.DurationMs, now)
			}
		}
		if _, err := decoder.Token(); err != nil {
			return err
		}
		if !decoder.More() {
			break
		}
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		var ok bool
		appID, ok = keyToken.(string)
		if !ok {
			return fmt.Errorf("duration telemetry: expected object key")
		}
	}
	_, err := decoder.Token()
	return err
}

func (t *DurationTelemetry) snapshotLocked() durationTelemetryDisk {
	snapshot := durationTelemetryDisk{
		Version: durationTelemetryDiskVersion,
		Workers: make(map[string][]durationBucket, len(t.workers)),
	}
	for appID, buckets := range t.workers {
		copied := make([]durationBucket, len(buckets))
		for index, bucket := range buckets {
			copied[index] = bucket
			copied[index].Samples = append([]float64(nil), bucket.Samples...)
		}
		snapshot.Workers[appID] = copied
	}
	return snapshot
}

func (t *DurationTelemetry) persistSnapshot(snapshot durationTelemetryDisk) error {
	if t.persistPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(t.persistPath), 0o700); err != nil {
		return err
	}
	tmpPath := t.persistPath + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	encodeErr := json.NewEncoder(file).Encode(snapshot)
	closeErr := file.Close()
	if encodeErr != nil {
		_ = os.Remove(tmpPath)
		return encodeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return closeErr
	}
	return os.Rename(tmpPath, t.persistPath)
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
