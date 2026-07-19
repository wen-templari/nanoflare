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
