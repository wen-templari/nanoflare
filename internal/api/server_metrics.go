package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func (s *Server) prometheusMetrics(w http.ResponseWriter, _ *http.Request) {
	var builder strings.Builder
	builder.WriteString("# HELP nanoflare_kv_reads_total Runtime KV read operations.\n")
	builder.WriteString("# TYPE nanoflare_kv_reads_total counter\n")
	builder.WriteString("# HELP nanoflare_kv_writes_total Runtime KV write operations.\n")
	builder.WriteString("# TYPE nanoflare_kv_writes_total counter\n")
	namespaces, err := s.service.ListKVNamespaces()
	if err == nil {
		for _, namespace := range namespaces {
			metrics, metricErr := s.service.KVNamespaceMetrics(namespace.ID)
			if metricErr != nil {
				continue
			}
			labels := fmt.Sprintf(`namespace_id="%s",namespace_name="%s"`, prometheusLabel(namespace.ID), prometheusLabel(namespace.Name))
			fmt.Fprintf(&builder, "nanoflare_kv_reads_total{%s} %d\n", labels, metrics.Reads)
			fmt.Fprintf(&builder, "nanoflare_kv_writes_total{%s} %d\n", labels, metrics.Writes)
		}
	}

	builder.WriteString("# HELP nanoflare_object_storage_reads_total Runtime object storage read operations.\n")
	builder.WriteString("# TYPE nanoflare_object_storage_reads_total counter\n")
	builder.WriteString("# HELP nanoflare_object_storage_writes_total Runtime object storage write operations.\n")
	builder.WriteString("# TYPE nanoflare_object_storage_writes_total counter\n")
	builder.WriteString("# HELP nanoflare_object_storage_size_bytes Stored object storage bytes by bucket.\n")
	builder.WriteString("# TYPE nanoflare_object_storage_size_bytes gauge\n")
	buckets, err := s.service.ListObjectStorageBuckets()
	if err == nil {
		for _, bucket := range buckets {
			metrics, metricErr := s.service.ObjectStorageBucketMetrics(bucket.ID)
			if metricErr != nil {
				continue
			}
			labels := fmt.Sprintf(`bucket_id="%s",bucket_name="%s"`, prometheusLabel(bucket.ID), prometheusLabel(bucket.Name))
			fmt.Fprintf(&builder, "nanoflare_object_storage_reads_total{%s} %d\n", labels, metrics.Reads)
			fmt.Fprintf(&builder, "nanoflare_object_storage_writes_total{%s} %d\n", labels, metrics.Writes)
			fmt.Fprintf(&builder, "nanoflare_object_storage_size_bytes{%s} %d\n", labels, metrics.Size)
		}
	}

	builder.WriteString("# HELP nanoflare_db_queries_total Runtime database query operations.\n")
	builder.WriteString("# TYPE nanoflare_db_queries_total counter\n")
	builder.WriteString("# HELP nanoflare_db_read_queries_total Runtime database read query operations.\n")
	builder.WriteString("# TYPE nanoflare_db_read_queries_total counter\n")
	builder.WriteString("# HELP nanoflare_db_write_queries_total Runtime database write query operations.\n")
	builder.WriteString("# TYPE nanoflare_db_write_queries_total counter\n")
	builder.WriteString("# HELP nanoflare_db_rows_read_total Runtime database rows read.\n")
	builder.WriteString("# TYPE nanoflare_db_rows_read_total counter\n")
	builder.WriteString("# HELP nanoflare_db_rows_written_total Runtime database rows written.\n")
	builder.WriteString("# TYPE nanoflare_db_rows_written_total counter\n")
	builder.WriteString("# HELP nanoflare_db_storage_size_bytes Stored database bytes.\n")
	builder.WriteString("# TYPE nanoflare_db_storage_size_bytes gauge\n")
	builder.WriteString("# HELP nanoflare_db_tables Database user table count.\n")
	builder.WriteString("# TYPE nanoflare_db_tables gauge\n")
	builder.WriteString("# HELP nanoflare_db_query_duration_seconds Runtime database query duration.\n")
	builder.WriteString("# TYPE nanoflare_db_query_duration_seconds histogram\n")
	databases, err := s.service.ListDatabases()
	if err == nil {
		for _, database := range databases {
			metrics, metricErr := s.service.DatabaseMetrics(database.ID)
			if metricErr != nil {
				continue
			}
			labels := fmt.Sprintf(`database_id="%s",database_name="%s"`, prometheusLabel(database.ID), prometheusLabel(database.Name))
			fmt.Fprintf(&builder, "nanoflare_db_queries_total{%s} %d\n", labels, metrics.Queries)
			fmt.Fprintf(&builder, "nanoflare_db_read_queries_total{%s} %d\n", labels, metrics.ReadQueries)
			fmt.Fprintf(&builder, "nanoflare_db_write_queries_total{%s} %d\n", labels, metrics.WriteQueries)
			fmt.Fprintf(&builder, "nanoflare_db_rows_read_total{%s} %d\n", labels, metrics.RowsRead)
			fmt.Fprintf(&builder, "nanoflare_db_rows_written_total{%s} %d\n", labels, metrics.RowsWritten)
			fmt.Fprintf(&builder, "nanoflare_db_storage_size_bytes{%s} %d\n", labels, metrics.StorageBytes)
			fmt.Fprintf(&builder, "nanoflare_db_tables{%s} %d\n", labels, metrics.TableCount)
			writeDatabaseDurationHistogram(&builder, labels, metrics)
		}
	}

	builder.WriteString("# HELP nanoflare_worker_gateway_requests_total Requests proxied from nanoflared to local worker runtimes.\n")
	builder.WriteString("# TYPE nanoflare_worker_gateway_requests_total counter\n")
	fmt.Fprintf(&builder, "nanoflare_worker_gateway_requests_total %d\n", s.workerGatewayMetrics.requests.Load())
	builder.WriteString("# HELP nanoflare_worker_gateway_errors_total Worker gateway proxy request errors.\n")
	builder.WriteString("# TYPE nanoflare_worker_gateway_errors_total counter\n")
	fmt.Fprintf(&builder, "nanoflare_worker_gateway_errors_total %d\n", s.workerGatewayMetrics.errors.Load())
	builder.WriteString("# HELP nanoflare_worker_gateway_connections_total Worker gateway connection acquisitions.\n")
	builder.WriteString("# TYPE nanoflare_worker_gateway_connections_total counter\n")
	fmt.Fprintf(&builder, "nanoflare_worker_gateway_connections_total %d\n", s.workerGatewayMetrics.connections.Load())
	builder.WriteString("# HELP nanoflare_worker_gateway_connections_reused_total Worker gateway connection acquisitions that reused an existing connection.\n")
	builder.WriteString("# TYPE nanoflare_worker_gateway_connections_reused_total counter\n")
	fmt.Fprintf(&builder, "nanoflare_worker_gateway_connections_reused_total %d\n", s.workerGatewayMetrics.reused.Load())
	builder.WriteString("# HELP nanoflare_worker_gateway_connections_was_idle_total Worker gateway reused connection acquisitions where the connection had been idle.\n")
	builder.WriteString("# TYPE nanoflare_worker_gateway_connections_was_idle_total counter\n")
	fmt.Fprintf(&builder, "nanoflare_worker_gateway_connections_was_idle_total %d\n", s.workerGatewayMetrics.idle.Load())

	if stats, ok := s.service.RepositoryPoolStats(); ok {
		builder.WriteString("# HELP nanoflare_repository_pool_max_open_connections Maximum number of open repository connections.\n")
		builder.WriteString("# TYPE nanoflare_repository_pool_max_open_connections gauge\n")
		fmt.Fprintf(&builder, "nanoflare_repository_pool_max_open_connections %d\n", stats.MaxOpenConnections)
		builder.WriteString("# HELP nanoflare_repository_pool_open_connections Open repository connections.\n")
		builder.WriteString("# TYPE nanoflare_repository_pool_open_connections gauge\n")
		fmt.Fprintf(&builder, "nanoflare_repository_pool_open_connections %d\n", stats.OpenConnections)
		builder.WriteString("# HELP nanoflare_repository_pool_in_use_connections Repository connections currently in use.\n")
		builder.WriteString("# TYPE nanoflare_repository_pool_in_use_connections gauge\n")
		fmt.Fprintf(&builder, "nanoflare_repository_pool_in_use_connections %d\n", stats.InUse)
		builder.WriteString("# HELP nanoflare_repository_pool_idle_connections Idle repository connections.\n")
		builder.WriteString("# TYPE nanoflare_repository_pool_idle_connections gauge\n")
		fmt.Fprintf(&builder, "nanoflare_repository_pool_idle_connections %d\n", stats.Idle)
		builder.WriteString("# HELP nanoflare_repository_pool_wait_total Repository connection pool waits.\n")
		builder.WriteString("# TYPE nanoflare_repository_pool_wait_total counter\n")
		fmt.Fprintf(&builder, "nanoflare_repository_pool_wait_total %d\n", stats.WaitCount)
		builder.WriteString("# HELP nanoflare_repository_pool_wait_duration_milliseconds_total Total time spent waiting for repository connections.\n")
		builder.WriteString("# TYPE nanoflare_repository_pool_wait_duration_milliseconds_total counter\n")
		fmt.Fprintf(&builder, "nanoflare_repository_pool_wait_duration_milliseconds_total %d\n", stats.WaitDurationMS)
		builder.WriteString("# HELP nanoflare_repository_pool_max_idle_closed_total Repository connections closed due to max idle count.\n")
		builder.WriteString("# TYPE nanoflare_repository_pool_max_idle_closed_total counter\n")
		fmt.Fprintf(&builder, "nanoflare_repository_pool_max_idle_closed_total %d\n", stats.MaxIdleClosed)
		builder.WriteString("# HELP nanoflare_repository_pool_max_idle_time_closed_total Repository connections closed due to max idle time.\n")
		builder.WriteString("# TYPE nanoflare_repository_pool_max_idle_time_closed_total counter\n")
		fmt.Fprintf(&builder, "nanoflare_repository_pool_max_idle_time_closed_total %d\n", stats.MaxIdleTimeClosed)
		builder.WriteString("# HELP nanoflare_repository_pool_max_lifetime_closed_total Repository connections closed due to max lifetime.\n")
		builder.WriteString("# TYPE nanoflare_repository_pool_max_lifetime_closed_total counter\n")
		fmt.Fprintf(&builder, "nanoflare_repository_pool_max_lifetime_closed_total %d\n", stats.MaxLifetimeClosed)
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(builder.String()))
}

func writeDatabaseDurationHistogram(builder *strings.Builder, labels string, metrics nanoflare.DatabaseMetrics) {
	cumulative := int64(0)
	for _, bucket := range []struct {
		le    string
		count int64
	}{
		{"0.0005", metrics.DurationBucket0_5},
		{"0.001", metrics.DurationBucket1},
		{"0.0025", metrics.DurationBucket2_5},
		{"0.005", metrics.DurationBucket5},
		{"0.01", metrics.DurationBucket10},
		{"0.025", metrics.DurationBucket25},
		{"0.05", metrics.DurationBucket50},
		{"0.1", metrics.DurationBucket100},
		{"0.25", metrics.DurationBucket250},
		{"0.5", metrics.DurationBucket500},
		{"1", metrics.DurationBucket1000},
		{"+Inf", metrics.DurationBucketInf},
	} {
		cumulative += bucket.count
		fmt.Fprintf(builder, "nanoflare_db_query_duration_seconds_bucket{%s,le=\"%s\"} %d\n", labels, bucket.le, cumulative)
	}
	fmt.Fprintf(builder, "nanoflare_db_query_duration_seconds_sum{%s} %.9f\n", labels, metrics.TotalDurationMS/1000)
	fmt.Fprintf(builder, "nanoflare_db_query_duration_seconds_count{%s} %d\n", labels, metrics.Queries)
}

func prometheusLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
