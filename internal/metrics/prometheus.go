package metrics

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

const prometheusQueryTimeout = 3 * time.Second

type Client struct {
	api     v1.API
	initErr error
	now     func() time.Time
}

func NewClient(baseURL string) *Client {
	client, err := api.NewClient(api.Config{Address: baseURL})
	if err != nil {
		return &Client{initErr: err, now: time.Now}
	}
	return &Client{api: v1.NewAPI(client), now: time.Now}
}

func (c *Client) Traffic(appID string) (nanoflare.WorkerTraffic, error) {
	router := strconv.Quote(regexp.QuoteMeta(appID) + `@(http|file)`)
	selector := `router=~` + router
	requests, err := c.query(`sum(rate(traefik_router_requests_total{` + selector + `}[5m]))`)
	if err != nil {
		return nanoflare.WorkerTraffic{}, err
	}
	latency, err := c.query(`histogram_quantile(0.95, sum by (le) (rate(traefik_router_request_duration_seconds_bucket{` + selector + `}[24h])))`)
	if err != nil {
		return nanoflare.WorkerTraffic{}, err
	}
	invocations, err := c.query(`sum(increase(traefik_router_requests_total{` + selector + `}[24h]))`)
	if err != nil {
		return nanoflare.WorkerTraffic{}, err
	}
	errorTotal, err := c.query(`sum(increase(traefik_router_requests_total{` + selector + `,code=~"5.."}[24h]))`)
	if err != nil {
		return nanoflare.WorkerTraffic{}, err
	}
	traffic, err := c.queryRange(`sum(increase(traefik_router_requests_total{` + selector + `}[5m]))`)
	if err != nil {
		return nanoflare.WorkerTraffic{}, err
	}
	statusCodes, err := c.query(`sum by (code) (increase(traefik_router_requests_total{` + selector + `}[24h]))`)
	if err != nil {
		return nanoflare.WorkerTraffic{}, err
	}
	requestRate := resultNumber(requests)
	result := nanoflare.WorkerTraffic{
		Available:         true,
		RequestsPerSecond: requestRate,
		P95Latency:        resultNumber(latency),
		Traffic:           resultValues(traffic),
		Invocations:       resultNumber(invocations),
		Errors:            resultNumber(errorTotal),
	}
	if result.Invocations > 0 {
		result.ErrorRate = result.Errors / result.Invocations
	}
	for _, item := range vector(statusCodes) {
		result.StatusCodes = append(result.StatusCodes, nanoflare.WorkerStatusCode{
			Code:  string(item.Metric["code"]),
			Value: float64(item.Value),
		})
	}
	sort.Slice(result.StatusCodes, func(i, j int) bool {
		return result.StatusCodes[i].Code < result.StatusCodes[j].Code
	})
	return result, nil
}

func (c *Client) DatabaseMetricsTimeseries(databaseID string) (nanoflare.DatabaseMetricsTimeseries, error) {
	selector := `database_id=` + strconv.Quote(databaseID)
	queries, err := c.queryRange(`sum(increase(nanoflare_db_queries_total{` + selector + `}[5m]))`)
	if err != nil {
		return nanoflare.DatabaseMetricsTimeseries{}, err
	}
	readQueries, err := c.queryRange(`sum(increase(nanoflare_db_read_queries_total{` + selector + `}[5m]))`)
	if err != nil {
		return nanoflare.DatabaseMetricsTimeseries{}, err
	}
	writeQueries, err := c.queryRange(`sum(increase(nanoflare_db_write_queries_total{` + selector + `}[5m]))`)
	if err != nil {
		return nanoflare.DatabaseMetricsTimeseries{}, err
	}
	rowsRead, err := c.queryRange(`sum(increase(nanoflare_db_rows_read_total{` + selector + `}[5m]))`)
	if err != nil {
		return nanoflare.DatabaseMetricsTimeseries{}, err
	}
	rowsReturned, err := c.queryRange(`sum(increase(nanoflare_db_rows_returned_total{` + selector + `}[5m]))`)
	if err != nil {
		return nanoflare.DatabaseMetricsTimeseries{}, err
	}
	rowsWritten, err := c.queryRange(`sum(increase(nanoflare_db_rows_written_total{` + selector + `}[5m]))`)
	if err != nil {
		return nanoflare.DatabaseMetricsTimeseries{}, err
	}
	durationTotal, err := c.queryRange(`sum(increase(nanoflare_db_query_duration_seconds_sum{` + selector + `}[5m])) * 1000`)
	if err != nil {
		return nanoflare.DatabaseMetricsTimeseries{}, err
	}
	storageBytes, err := c.queryRange(`max(nanoflare_db_storage_size_bytes{` + selector + `})`)
	if err != nil {
		return nanoflare.DatabaseMetricsTimeseries{}, err
	}
	tableCount, err := c.queryRange(`max(nanoflare_db_tables{` + selector + `})`)
	if err != nil {
		return nanoflare.DatabaseMetricsTimeseries{}, err
	}
	p50Latency, err := c.queryRange(`histogram_quantile(0.50, sum by (le) (rate(nanoflare_db_query_duration_seconds_bucket{` + selector + `}[5m]))) * 1000`)
	if err != nil {
		return nanoflare.DatabaseMetricsTimeseries{}, err
	}
	p95Latency, err := c.queryRange(`histogram_quantile(0.95, sum by (le) (rate(nanoflare_db_query_duration_seconds_bucket{` + selector + `}[5m]))) * 1000`)
	if err != nil {
		return nanoflare.DatabaseMetricsTimeseries{}, err
	}
	p99Latency, err := c.queryRange(`histogram_quantile(0.99, sum by (le) (rate(nanoflare_db_query_duration_seconds_bucket{` + selector + `}[5m]))) * 1000`)
	if err != nil {
		return nanoflare.DatabaseMetricsTimeseries{}, err
	}
	return nanoflare.DatabaseMetricsTimeseries{
		Available:       true,
		Queries:         resultPoints(queries),
		ReadQueries:     resultPoints(readQueries),
		WriteQueries:    resultPoints(writeQueries),
		RowsRead:        resultPoints(rowsRead),
		RowsReturned:    resultPoints(rowsReturned),
		RowsWritten:     resultPoints(rowsWritten),
		StorageBytes:    resultPoints(storageBytes),
		TableCount:      resultPoints(tableCount),
		DurationTotalMS: resultPoints(durationTotal),
		P50LatencyMS:    resultPoints(p50Latency),
		P95LatencyMS:    resultPoints(p95Latency),
		P99LatencyMS:    resultPoints(p99Latency),
	}, nil
}

func (c *Client) KVNamespaceMetricsTimeseries(namespaceID string) (nanoflare.KVNamespaceMetricsTimeseries, error) {
	selector := `namespace_id=` + strconv.Quote(namespaceID)
	reads, err := c.queryRange(`sum(increase(nanoflare_kv_reads_total{` + selector + `}[5m]))`)
	if err != nil {
		return nanoflare.KVNamespaceMetricsTimeseries{}, err
	}
	writes, err := c.queryRange(`sum(increase(nanoflare_kv_writes_total{` + selector + `}[5m]))`)
	if err != nil {
		return nanoflare.KVNamespaceMetricsTimeseries{}, err
	}
	size, err := c.queryRange(`max(nanoflare_kv_size_bytes{` + selector + `})`)
	if err != nil {
		return nanoflare.KVNamespaceMetricsTimeseries{}, err
	}
	return nanoflare.KVNamespaceMetricsTimeseries{Available: true, Reads: resultPoints(reads), Writes: resultPoints(writes), Size: resultPoints(size)}, nil
}

func (c *Client) ObjectStorageBucketMetricsTimeseries(bucketID string) (nanoflare.ObjectStorageBucketMetricsTimeseries, error) {
	selector := `bucket_id=` + strconv.Quote(bucketID)
	reads, err := c.queryRange(`sum(increase(nanoflare_object_storage_reads_total{` + selector + `}[5m]))`)
	if err != nil {
		return nanoflare.ObjectStorageBucketMetricsTimeseries{}, err
	}
	writes, err := c.queryRange(`sum(increase(nanoflare_object_storage_writes_total{` + selector + `}[5m]))`)
	if err != nil {
		return nanoflare.ObjectStorageBucketMetricsTimeseries{}, err
	}
	size, err := c.queryRange(`max(nanoflare_object_storage_size_bytes{` + selector + `})`)
	if err != nil {
		return nanoflare.ObjectStorageBucketMetricsTimeseries{}, err
	}
	return nanoflare.ObjectStorageBucketMetricsTimeseries{Available: true, Reads: resultPoints(reads), Writes: resultPoints(writes), Size: resultPoints(size)}, nil
}

func (c *Client) WorkerMetricsTimeseries(appID string) (nanoflare.WorkerMetricsTimeseries, error) {
	router := strconv.Quote(regexp.QuoteMeta(appID) + `@(http|file)`)
	routerSelector := `router=~` + router
	points := func(query string) ([]nanoflare.MetricPoint, error) {
		result, err := c.queryRange(query)
		return resultPoints(result), err
	}
	requests, err := points(`sum(increase(traefik_router_requests_total{` + routerSelector + `}[5m]))`)
	if err != nil {
		return nanoflare.WorkerMetricsTimeseries{}, err
	}
	errors, err := points(`sum(increase(traefik_router_requests_total{` + routerSelector + `,code=~"5.."}[5m]))`)
	if err != nil {
		return nanoflare.WorkerMetricsTimeseries{}, err
	}
	errorRate, err := points(`sum(increase(traefik_router_requests_total{` + routerSelector + `,code=~"5.."}[5m])) / sum(increase(traefik_router_requests_total{` + routerSelector + `}[5m]))`)
	if err != nil {
		return nanoflare.WorkerMetricsTimeseries{}, err
	}
	p95, err := points(`histogram_quantile(0.95, sum by (le) (rate(traefik_router_request_duration_seconds_bucket{` + routerSelector + `}[5m]))) * 1000`)
	if err != nil {
		return nanoflare.WorkerMetricsTimeseries{}, err
	}
	durationSelector := `worker_id=` + strconv.Quote(appID)
	avg, err := points(`max(nanoflare_worker_duration_milliseconds_average{` + durationSelector + `})`)
	if err != nil {
		return nanoflare.WorkerMetricsTimeseries{}, err
	}
	durationP95, err := points(`max(nanoflare_worker_duration_milliseconds_p95{` + durationSelector + `})`)
	if err != nil {
		return nanoflare.WorkerMetricsTimeseries{}, err
	}
	perSecond, err := points(`max(nanoflare_worker_duration_milliseconds_per_second{` + durationSelector + `})`)
	if err != nil {
		return nanoflare.WorkerMetricsTimeseries{}, err
	}
	status, err := c.queryRange(`sum by (code) (increase(traefik_router_requests_total{` + routerSelector + `}[5m]))`)
	if err != nil {
		return nanoflare.WorkerMetricsTimeseries{}, err
	}
	statusCodes := make([]nanoflare.WorkerStatusCodeTimeseries, 0, len(matrix(status)))
	for _, series := range matrix(status) {
		statusCodes = append(statusCodes, nanoflare.WorkerStatusCodeTimeseries{Code: string(series.Metric["code"]), Points: pointsForStream(series)})
	}
	sort.Slice(statusCodes, func(i, j int) bool { return statusCodes[i].Code < statusCodes[j].Code })
	return nanoflare.WorkerMetricsTimeseries{Available: true, Requests: requests, Errors: errors, ErrorRate: errorRate, P95LatencyMS: p95, StatusCodes: statusCodes, DurationAvgMS: avg, DurationP95MS: durationP95, DurationMSPerSecond: perSecond}, nil
}

func (c *Client) query(query string) (model.Value, error) {
	if c.initErr != nil {
		return nil, fmt.Errorf("create prometheus client: %w", c.initErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), prometheusQueryTimeout)
	defer cancel()
	result, _, err := c.api.Query(ctx, query, c.now())
	if err != nil {
		return nil, fmt.Errorf("query prometheus: %w", err)
	}
	return result, nil
}

func (c *Client) queryRange(query string) (model.Value, error) {
	if c.initErr != nil {
		return nil, fmt.Errorf("create prometheus client: %w", c.initErr)
	}
	end := c.now()
	ctx, cancel := context.WithTimeout(context.Background(), prometheusQueryTimeout)
	defer cancel()
	result, _, err := c.api.QueryRange(ctx, query, v1.Range{Start: end.Add(-24 * time.Hour), End: end, Step: 5 * time.Minute})
	if err != nil {
		return nil, fmt.Errorf("query prometheus: %w", err)
	}
	return result, nil
}

func resultNumber(result model.Value) float64 {
	values := vector(result)
	if len(values) == 0 {
		return 0
	}
	value := float64(values[0].Value)
	if !isFinite(value) {
		return 0
	}
	return value
}

func resultValues(result model.Value) []float64 {
	values := matrix(result)
	if len(values) == 0 {
		return []float64{}
	}
	points := values[0].Values
	resultValues := make([]float64, 0, len(points))
	for _, point := range points {
		value := float64(point.Value)
		if isFinite(value) {
			resultValues = append(resultValues, value)
		}
	}
	return resultValues
}

func resultPoints(result model.Value) []nanoflare.MetricPoint {
	values := matrix(result)
	if len(values) == 0 {
		return []nanoflare.MetricPoint{}
	}
	return pointsForStream(values[0])
}

func pointsForStream(stream *model.SampleStream) []nanoflare.MetricPoint {
	points := make([]nanoflare.MetricPoint, 0, len(stream.Values))
	for _, value := range stream.Values {
		metricValue := float64(value.Value)
		if !isFinite(metricValue) {
			continue
		}
		points = append(points, nanoflare.MetricPoint{Timestamp: value.Timestamp.Time().UTC(), Value: metricValue})
	}
	return points
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func vector(result model.Value) model.Vector {
	values, _ := result.(model.Vector)
	return values
}

func matrix(result model.Value) model.Matrix {
	values, _ := result.(model.Matrix)
	return values
}
