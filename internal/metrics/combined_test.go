package metrics

import (
	"errors"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

type fakePrometheusReader struct {
	traffic nanoflare.WorkerTraffic
	err     error
}

func (r fakePrometheusReader) Traffic(string) (nanoflare.WorkerTraffic, error) {
	return r.traffic, r.err
}

type fakeDurationReader struct {
	traffic runtime.DurationStats
}

func (r fakeDurationReader) Stats(string) runtime.DurationStats {
	return r.traffic
}

func TestCombinedReaderMergesPrometheusAndDurationStats(t *testing.T) {
	reader := NewCombinedReader(
		fakePrometheusReader{traffic: nanoflare.WorkerTraffic{Available: true, RequestsPerSecond: 4.25}},
		fakeDurationReader{traffic: runtime.DurationStats{DurationMsAvg: 12, DurationMsP95: 18, DurationMsPerSecond: 2.5, DurationSeries: []float64{1, 2}}},
	)
	traffic, err := reader.Traffic("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !traffic.Available || traffic.RequestsPerSecond != 4.25 {
		t.Fatalf("unexpected prometheus metrics: %#v", traffic)
	}
	if traffic.DurationMsAvg != 12 || traffic.DurationMsP95 != 18 || traffic.DurationMsPerSecond != 2.5 {
		t.Fatalf("unexpected duration metrics: %#v", traffic)
	}
	if len(traffic.DurationSeries) != 2 {
		t.Fatalf("series = %#v", traffic.DurationSeries)
	}
}

func TestCombinedReaderReturnsDurationsWhenPrometheusFails(t *testing.T) {
	reader := NewCombinedReader(
		fakePrometheusReader{err: errors.New("boom")},
		fakeDurationReader{traffic: runtime.DurationStats{DurationMsAvg: 7}},
	)
	traffic, err := reader.Traffic("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if traffic.Available {
		t.Fatalf("available = true, want false when prometheus fails")
	}
	if traffic.DurationMsAvg != 7 {
		t.Fatalf("duration avg = %v, want 7", traffic.DurationMsAvg)
	}
}
