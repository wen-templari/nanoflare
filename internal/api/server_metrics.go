package api

import (
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/prometheus/client_golang/prometheus"
)

type serverMetricsCollector struct {
	server *Server
}

func (serverMetricsCollector) Describe(chan<- *prometheus.Desc) {}

func (c serverMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.server
	collectKVNamespaceMetrics(ch, s)
	collectObjectStorageMetrics(ch, s)
	collectDatabaseMetrics(ch, s)
	collectWorkerDurationMetrics(ch, s)
	collectWorkerGatewayMetrics(ch, s)
	collectRepositoryPoolMetrics(ch, s)
	collectOrganizationMetrics(ch, s)
}

func metricDesc(name, help string, labels []string) *prometheus.Desc {
	return prometheus.NewDesc(name, help, labels, nil)
}

func collectKVNamespaceMetrics(ch chan<- prometheus.Metric, s *Server) {
	reads := metricDesc("nanoflare_kv_reads_total", "Runtime KV read operations.", []string{"namespace_id", "namespace_name"})
	writes := metricDesc("nanoflare_kv_writes_total", "Runtime KV write operations.", []string{"namespace_id", "namespace_name"})
	size := metricDesc("nanoflare_kv_size_bytes", "Stored KV bytes by namespace.", []string{"namespace_id", "namespace_name"})
	namespaces, err := s.service.ListKVNamespaces()
	if err != nil {
		return
	}
	for _, namespace := range namespaces {
		metrics, err := s.service.KVNamespaceMetrics(namespace.ID)
		if err != nil {
			continue
		}
		labels := []string{namespace.ID, namespace.Name}
		ch <- prometheus.MustNewConstMetric(reads, prometheus.CounterValue, float64(metrics.Reads), labels...)
		ch <- prometheus.MustNewConstMetric(writes, prometheus.CounterValue, float64(metrics.Writes), labels...)
		ch <- prometheus.MustNewConstMetric(size, prometheus.GaugeValue, float64(metrics.Size), labels...)
	}
}

func collectObjectStorageMetrics(ch chan<- prometheus.Metric, s *Server) {
	reads := metricDesc("nanoflare_object_storage_reads_total", "Runtime object storage read operations.", []string{"bucket_id", "bucket_name"})
	writes := metricDesc("nanoflare_object_storage_writes_total", "Runtime object storage write operations.", []string{"bucket_id", "bucket_name"})
	size := metricDesc("nanoflare_object_storage_size_bytes", "Stored object storage bytes by bucket.", []string{"bucket_id", "bucket_name"})
	buckets, err := s.service.ListObjectStorageBuckets()
	if err != nil {
		return
	}
	for _, bucket := range buckets {
		metrics, err := s.service.ObjectStorageBucketMetrics(bucket.ID)
		if err != nil {
			continue
		}
		labels := []string{bucket.ID, bucket.Name}
		ch <- prometheus.MustNewConstMetric(reads, prometheus.CounterValue, float64(metrics.Reads), labels...)
		ch <- prometheus.MustNewConstMetric(writes, prometheus.CounterValue, float64(metrics.Writes), labels...)
		ch <- prometheus.MustNewConstMetric(size, prometheus.GaugeValue, float64(metrics.Size), labels...)
	}
}

func collectDatabaseMetrics(ch chan<- prometheus.Metric, s *Server) {
	labels := []string{"database_id", "database_name"}
	queries := metricDesc("nanoflare_db_queries_total", "Runtime database query operations.", labels)
	readQueries := metricDesc("nanoflare_db_read_queries_total", "Runtime database read query operations.", labels)
	writeQueries := metricDesc("nanoflare_db_write_queries_total", "Runtime database write query operations.", labels)
	rowsRead := metricDesc("nanoflare_db_rows_read_total", "Runtime database rows read.", labels)
	rowsReturned := metricDesc("nanoflare_db_rows_returned_total", "Runtime database rows returned.", labels)
	rowsWritten := metricDesc("nanoflare_db_rows_written_total", "Runtime database rows written.", labels)
	storage := metricDesc("nanoflare_db_storage_size_bytes", "Stored database bytes.", labels)
	tables := metricDesc("nanoflare_db_tables", "Database user table count.", labels)
	duration := metricDesc("nanoflare_db_query_duration_seconds", "Runtime database query duration.", labels)
	databases, err := s.service.ListDatabases()
	if err != nil {
		return
	}
	for _, database := range databases {
		metrics, err := s.service.DatabaseMetrics(database.ID)
		if err != nil {
			continue
		}
		values := []string{database.ID, database.Name}
		ch <- prometheus.MustNewConstMetric(queries, prometheus.CounterValue, float64(metrics.Queries), values...)
		ch <- prometheus.MustNewConstMetric(readQueries, prometheus.CounterValue, float64(metrics.ReadQueries), values...)
		ch <- prometheus.MustNewConstMetric(writeQueries, prometheus.CounterValue, float64(metrics.WriteQueries), values...)
		ch <- prometheus.MustNewConstMetric(rowsRead, prometheus.CounterValue, float64(metrics.RowsRead), values...)
		ch <- prometheus.MustNewConstMetric(rowsReturned, prometheus.CounterValue, float64(metrics.RowsReturned), values...)
		ch <- prometheus.MustNewConstMetric(rowsWritten, prometheus.CounterValue, float64(metrics.RowsWritten), values...)
		ch <- prometheus.MustNewConstMetric(storage, prometheus.GaugeValue, float64(metrics.StorageBytes), values...)
		ch <- prometheus.MustNewConstMetric(tables, prometheus.GaugeValue, float64(metrics.TableCount), values...)
		ch <- databaseDurationHistogram(duration, values, metrics)
	}
}

func databaseDurationHistogram(desc *prometheus.Desc, labels []string, metrics nanoflare.DatabaseMetrics) prometheus.Metric {
	cumulative := uint64(0)
	buckets := make(map[float64]uint64, 11)
	for _, bucket := range []struct {
		upperBound float64
		count      int64
	}{
		{0.0005, metrics.DurationBucket0_5}, {0.001, metrics.DurationBucket1}, {0.0025, metrics.DurationBucket2_5},
		{0.005, metrics.DurationBucket5}, {0.01, metrics.DurationBucket10}, {0.025, metrics.DurationBucket25},
		{0.05, metrics.DurationBucket50}, {0.1, metrics.DurationBucket100}, {0.25, metrics.DurationBucket250},
		{0.5, metrics.DurationBucket500}, {1, metrics.DurationBucket1000},
	} {
		cumulative += uint64(bucket.count)
		buckets[bucket.upperBound] = cumulative
	}
	return prometheus.MustNewConstHistogram(desc, uint64(metrics.Queries), metrics.TotalDurationMS/1000, buckets, labels...)
}

func collectWorkerDurationMetrics(ch chan<- prometheus.Metric, s *Server) {
	if s.durationTelemetry == nil {
		return
	}
	average := metricDesc("nanoflare_worker_duration_milliseconds_average", "Rolling average worker execution duration.", []string{"worker_id", "worker_name"})
	p95 := metricDesc("nanoflare_worker_duration_milliseconds_p95", "Rolling p95 worker execution duration.", []string{"worker_id", "worker_name"})
	perSecond := metricDesc("nanoflare_worker_duration_milliseconds_per_second", "Rolling worker execution time per second.", []string{"worker_id", "worker_name"})
	workers, err := s.service.ListApps()
	if err != nil {
		return
	}
	for _, worker := range workers {
		stats := s.durationTelemetry.Stats(worker.ID)
		if !stats.Available {
			continue
		}
		labels := []string{worker.ID, worker.Name}
		ch <- prometheus.MustNewConstMetric(average, prometheus.GaugeValue, stats.DurationMsAvg, labels...)
		ch <- prometheus.MustNewConstMetric(p95, prometheus.GaugeValue, stats.DurationMsP95, labels...)
		ch <- prometheus.MustNewConstMetric(perSecond, prometheus.GaugeValue, stats.DurationMsPerSecond, labels...)
	}
}

func collectWorkerGatewayMetrics(ch chan<- prometheus.Metric, s *Server) {
	for _, metric := range []struct {
		name  string
		help  string
		value int64
	}{
		{"nanoflare_worker_gateway_requests_total", "Requests proxied from nanoflared to local worker runtimes.", s.workerGatewayMetrics.requests.Load()},
		{"nanoflare_worker_gateway_errors_total", "Worker gateway proxy request errors.", s.workerGatewayMetrics.errors.Load()},
		{"nanoflare_worker_gateway_connections_total", "Worker gateway connection acquisitions.", s.workerGatewayMetrics.connections.Load()},
		{"nanoflare_worker_gateway_connections_reused_total", "Worker gateway connection acquisitions that reused an existing connection.", s.workerGatewayMetrics.reused.Load()},
		{"nanoflare_worker_gateway_connections_was_idle_total", "Worker gateway reused connection acquisitions where the connection had been idle.", s.workerGatewayMetrics.idle.Load()},
	} {
		ch <- prometheus.MustNewConstMetric(metricDesc(metric.name, metric.help, nil), prometheus.CounterValue, float64(metric.value))
	}
}

func collectRepositoryPoolMetrics(ch chan<- prometheus.Metric, s *Server) {
	stats, ok := s.service.RepositoryPoolStats()
	if !ok {
		return
	}
	for _, metric := range []struct {
		name  string
		help  string
		value int64
		type_ prometheus.ValueType
	}{
		{"nanoflare_repository_pool_max_open_connections", "Maximum number of open repository connections.", int64(stats.MaxOpenConnections), prometheus.GaugeValue},
		{"nanoflare_repository_pool_open_connections", "Open repository connections.", int64(stats.OpenConnections), prometheus.GaugeValue},
		{"nanoflare_repository_pool_in_use_connections", "Repository connections currently in use.", int64(stats.InUse), prometheus.GaugeValue},
		{"nanoflare_repository_pool_idle_connections", "Idle repository connections.", int64(stats.Idle), prometheus.GaugeValue},
		{"nanoflare_repository_pool_wait_total", "Repository connection pool waits.", stats.WaitCount, prometheus.CounterValue},
		{"nanoflare_repository_pool_wait_duration_milliseconds_total", "Total time spent waiting for repository connections.", stats.WaitDurationMS, prometheus.CounterValue},
		{"nanoflare_repository_pool_max_idle_closed_total", "Repository connections closed due to max idle count.", stats.MaxIdleClosed, prometheus.CounterValue},
		{"nanoflare_repository_pool_max_idle_time_closed_total", "Repository connections closed due to max idle time.", stats.MaxIdleTimeClosed, prometheus.CounterValue},
		{"nanoflare_repository_pool_max_lifetime_closed_total", "Repository connections closed due to max lifetime.", stats.MaxLifetimeClosed, prometheus.CounterValue},
	} {
		ch <- prometheus.MustNewConstMetric(metricDesc(metric.name, metric.help, nil), metric.type_, float64(metric.value))
	}
}

func collectOrganizationMetrics(ch chan<- prometheus.Metric, s *Server) {
	organizations, err := s.service.ListOrganizations()
	if err != nil {
		return
	}
	ch <- prometheus.MustNewConstMetric(metricDesc("nanoflare_organizations", "Total organizations.", nil), prometheus.GaugeValue, float64(len(organizations)))
	if users, err := s.service.UserCount(); err == nil {
		ch <- prometheus.MustNewConstMetric(metricDesc("nanoflare_users", "Total users.", nil), prometheus.GaugeValue, float64(users))
	}

	workers, err := s.service.ListApps()
	if err != nil {
		return
	}
	namespaces, err := s.service.ListKVNamespaces()
	if err != nil {
		return
	}
	databases, err := s.service.ListDatabases()
	if err != nil {
		return
	}
	buckets, err := s.service.ListObjectStorageBuckets()
	if err != nil {
		return
	}

	type resourceCounts struct{ workers, namespaces, databases, buckets int }
	counts := make(map[string]*resourceCounts, len(organizations))
	for _, org := range organizations {
		counts[org.ID] = &resourceCounts{}
	}
	for _, worker := range workers {
		if count := counts[worker.OrgID]; count != nil {
			count.workers++
		}
	}
	for _, namespace := range namespaces {
		if count := counts[namespace.OrgID]; count != nil {
			count.namespaces++
		}
	}
	for _, database := range databases {
		if count := counts[database.OrgID]; count != nil {
			count.databases++
		}
	}
	for _, bucket := range buckets {
		if count := counts[bucket.OrgID]; count != nil {
			count.buckets++
		}
	}

	labels := []string{"organization_id", "organization_name"}
	members := metricDesc("nanoflare_organization_members", "Organization members.", labels)
	workerCount := metricDesc("nanoflare_organization_workers", "Workers owned by an organization.", labels)
	namespaceCount := metricDesc("nanoflare_organization_kv_namespaces", "KV namespaces owned by an organization.", labels)
	databaseCount := metricDesc("nanoflare_organization_databases", "Databases owned by an organization.", labels)
	bucketCount := metricDesc("nanoflare_organization_object_storage_buckets", "Object storage buckets owned by an organization.", labels)
	for _, org := range organizations {
		memberCount, err := s.service.ListOrganizationMembers(org.ID)
		if err != nil {
			continue
		}
		values := []string{org.ID, org.Name}
		count := counts[org.ID]
		ch <- prometheus.MustNewConstMetric(members, prometheus.GaugeValue, float64(len(memberCount)), values...)
		ch <- prometheus.MustNewConstMetric(workerCount, prometheus.GaugeValue, float64(count.workers), values...)
		ch <- prometheus.MustNewConstMetric(namespaceCount, prometheus.GaugeValue, float64(count.namespaces), values...)
		ch <- prometheus.MustNewConstMetric(databaseCount, prometheus.GaugeValue, float64(count.databases), values...)
		ch <- prometheus.MustNewConstMetric(bucketCount, prometheus.GaugeValue, float64(count.buckets), values...)
	}
}
