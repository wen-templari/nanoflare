package metrics

import (
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

type trafficReader interface {
	Traffic(string) (nanoflare.WorkerTraffic, error)
}

type durationStatsReader interface {
	Stats(string) runtime.DurationStats
}

type CombinedReader struct {
	prometheus trafficReader
	durations  durationStatsReader
}

func NewCombinedReader(prometheus trafficReader, durations durationStatsReader) *CombinedReader {
	return &CombinedReader{prometheus: prometheus, durations: durations}
}

func (r *CombinedReader) Traffic(appID string) (nanoflare.WorkerTraffic, error) {
	var result nanoflare.WorkerTraffic
	if r.prometheus != nil {
		traffic, err := r.prometheus.Traffic(appID)
		if err == nil {
			result = traffic
		}
	}
	if r.durations != nil {
		durations := r.durations.Stats(appID)
		result.DurationMsAvg = durations.DurationMsAvg
		result.DurationMsP95 = durations.DurationMsP95
		result.DurationMsPerSecond = durations.DurationMsPerSecond
		result.DurationSeries = durations.DurationSeries
	}
	return result, nil
}
