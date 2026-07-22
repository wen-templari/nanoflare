package runtime

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestDurationTelemetryComputesRollingStats(t *testing.T) {
	telemetry := NewDurationTelemetry()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	telemetry.now = func() time.Time { return now }
	telemetry.RecordBatch([]DurationTraceEvent{
		{ScriptName: "alpha", EventTimestamp: float64(now.Add(-4 * time.Minute).UnixMilli()), DurationMs: 10},
		{ScriptName: "alpha", EventTimestamp: float64(now.Add(-3 * time.Minute).UnixMilli()), DurationMs: 20},
		{ScriptName: "alpha", EventTimestamp: float64(now.Add(-2 * time.Minute).UnixMilli()), DurationMs: 30},
		{ScriptName: "beta", EventTimestamp: float64(now.Add(-2 * time.Minute).UnixMilli()), DurationMs: 99},
	})

	stats := telemetry.Stats("alpha")
	if !stats.Available {
		t.Fatal("expected alpha stats to be available")
	}
	if stats.DurationMsAvg != 20 {
		t.Fatalf("avg = %v, want 20", stats.DurationMsAvg)
	}
	if stats.DurationMsP95 != 30 {
		t.Fatalf("p95 = %v, want 30", stats.DurationMsP95)
	}
	if got, want := stats.DurationMsPerSecond, 60.0/(24.0*60.0*60.0); got != want {
		t.Fatalf("duration/sec = %v, want %v", got, want)
	}
	if len(stats.DurationSeries) != 1 || stats.DurationSeries[0] != 60.0/300.0 {
		t.Fatalf("series = %#v", stats.DurationSeries)
	}
}

func TestDurationTelemetryEvictsExpiredSamples(t *testing.T) {
	telemetry := NewDurationTelemetry()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	telemetry.now = func() time.Time { return now }
	telemetry.RecordBatch([]DurationTraceEvent{
		{ScriptName: "alpha", EventTimestamp: float64(now.Add(-25 * time.Hour).UnixMilli()), DurationMs: 10},
		{ScriptName: "alpha", EventTimestamp: float64(now.Add(-2 * time.Minute).UnixMilli()), DurationMs: 30},
	})

	stats := telemetry.Stats("alpha")
	if !stats.Available {
		t.Fatal("expected stats to stay available")
	}
	if stats.DurationMsAvg != 30 || stats.DurationMsP95 != 30 {
		t.Fatalf("unexpected stats after prune: %#v", stats)
	}
}

func TestDurationTelemetryIsolatesWorkers(t *testing.T) {
	telemetry := NewDurationTelemetry()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	telemetry.now = func() time.Time { return now }
	telemetry.RecordBatch([]DurationTraceEvent{
		{ScriptName: "alpha", EventTimestamp: float64(now.Add(-4 * time.Minute).UnixMilli()), DurationMs: 10},
		{ScriptName: "beta", EventTimestamp: float64(now.Add(-4 * time.Minute).UnixMilli()), DurationMs: 50},
	})

	if got, want := telemetry.Stats("alpha").DurationMsAvg, 10.0; got != want {
		t.Fatalf("alpha avg = %v, want %v", got, want)
	}
	if got, want := telemetry.Stats("beta").DurationMsAvg, 50.0; got != want {
		t.Fatalf("beta avg = %v, want %v", got, want)
	}
	if got := telemetry.Stats("missing"); !reflect.DeepEqual(got, DurationStats{}) {
		t.Fatalf("missing stats = %#v, want zero value", got)
	}
}

func TestPersistentDurationTelemetrySurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "duration-telemetry.json")
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	telemetry, err := NewPersistentDurationTelemetry(path)
	if err != nil {
		t.Fatal(err)
	}
	telemetry.now = func() time.Time { return now }
	telemetry.RecordBatch([]DurationTraceEvent{
		{ScriptName: "alpha", EventTimestamp: float64(now.Add(-30 * time.Minute).UnixMilli()), DurationMs: 15},
	})

	restarted, err := NewPersistentDurationTelemetry(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = func() time.Time { return now }
	stats := restarted.Stats("alpha")
	if !stats.Available || stats.DurationMsAvg != 15 || stats.DurationMsP95 != 15 {
		t.Fatalf("persisted stats = %#v", stats)
	}
}

func BenchmarkDurationTelemetryRecordBatchInMemory(b *testing.B) {
	telemetry := NewDurationTelemetry()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	telemetry.now = func() time.Time { return now }
	events := []DurationTraceEvent{{ScriptName: "alpha", EventTimestamp: float64(now.UnixMilli()), DurationMs: 12}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		telemetry.RecordBatch(events)
	}
}

func BenchmarkDurationTelemetryRecordBatchPersistent(b *testing.B) {
	telemetry, err := NewPersistentDurationTelemetry(filepath.Join(b.TempDir(), "duration-telemetry.json"))
	if err != nil {
		b.Fatal(err)
	}
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	telemetry.window = 100 * time.Millisecond

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now := base.Add(time.Duration(i) * time.Millisecond)
		telemetry.now = func() time.Time { return now }
		telemetry.RecordBatch([]DurationTraceEvent{{ScriptName: "alpha", EventTimestamp: float64(now.UnixMilli()), DurationMs: 12}})
	}
}

func BenchmarkDurationTelemetryStats(b *testing.B) {
	telemetry := NewDurationTelemetry()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	telemetry.now = func() time.Time { return now }
	events := make([]DurationTraceEvent, 1000)
	for i := range events {
		events[i] = DurationTraceEvent{ScriptName: "alpha", EventTimestamp: float64(now.Add(-time.Duration(i) * time.Second).UnixMilli()), DurationMs: float64(i%100 + 1)}
	}
	telemetry.RecordBatch(events)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = telemetry.Stats("alpha")
	}
}
