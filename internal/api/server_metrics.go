package api

import (
	"fmt"
	"net/http"
	"strings"
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

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(builder.String()))
}

func prometheusLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
