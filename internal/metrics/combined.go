package metrics

import (
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

type trafficReader interface {
	Traffic(string) (nanoflare.WorkerTraffic, error)
}

type databaseMetricsTimeseriesReader interface {
	DatabaseMetricsTimeseries(string) (nanoflare.DatabaseMetricsTimeseries, error)
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

func (r *CombinedReader) DatabaseMetricsTimeseries(databaseID string) (nanoflare.DatabaseMetricsTimeseries, error) {
	if prometheus, ok := r.prometheus.(databaseMetricsTimeseriesReader); ok {
		return prometheus.DatabaseMetricsTimeseries(databaseID)
	}
	return nanoflare.DatabaseMetricsTimeseries{}, nil
}

func (r *CombinedReader) KVNamespaceMetricsTimeseries(namespaceID string) (nanoflare.KVNamespaceMetricsTimeseries, error) {
	if prometheus, ok := r.prometheus.(interface {
		KVNamespaceMetricsTimeseries(string) (nanoflare.KVNamespaceMetricsTimeseries, error)
	}); ok {
		return prometheus.KVNamespaceMetricsTimeseries(namespaceID)
	}
	return nanoflare.KVNamespaceMetricsTimeseries{}, nil
}

func (r *CombinedReader) ObjectStorageBucketMetricsTimeseries(bucketID string) (nanoflare.ObjectStorageBucketMetricsTimeseries, error) {
	if prometheus, ok := r.prometheus.(interface {
		ObjectStorageBucketMetricsTimeseries(string) (nanoflare.ObjectStorageBucketMetricsTimeseries, error)
	}); ok {
		return prometheus.ObjectStorageBucketMetricsTimeseries(bucketID)
	}
	return nanoflare.ObjectStorageBucketMetricsTimeseries{}, nil
}

func (r *CombinedReader) WorkerMetricsTimeseries(appID string) (nanoflare.WorkerMetricsTimeseries, error) {
	if prometheus, ok := r.prometheus.(interface {
		WorkerMetricsTimeseries(string) (nanoflare.WorkerMetricsTimeseries, error)
	}); ok {
		return prometheus.WorkerMetricsTimeseries(appID)
	}
	return nanoflare.WorkerMetricsTimeseries{}, nil
}

func (r *CombinedReader) OrganizationWorkerMetricsTimeseries(appIDs []string) (nanoflare.WorkerMetricsTimeseries, error) {
	if prometheus, ok := r.prometheus.(interface {
		OrganizationWorkerMetricsTimeseries([]string) (nanoflare.WorkerMetricsTimeseries, error)
	}); ok {
		return prometheus.OrganizationWorkerMetricsTimeseries(appIDs)
	}
	return nanoflare.WorkerMetricsTimeseries{}, nil
}
