package metrics

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type Client struct {
	BaseURL string
	Client  *http.Client
}

type prometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []prometheusResult `json:"result"`
	} `json:"data"`
}

type prometheusResult struct {
	Metric map[string]string `json:"metric"`
	Value  []any             `json:"value"`
	Values [][]any           `json:"values"`
}

func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: baseURL,
		Client:  &http.Client{Timeout: 3 * time.Second},
	}
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
		StatusCodes:       make([]nanoflare.WorkerStatusCode, 0, len(statusCodes)),
	}
	if result.Invocations > 0 {
		result.ErrorRate = result.Errors / result.Invocations
	}
	for _, item := range statusCodes {
		result.StatusCodes = append(result.StatusCodes, nanoflare.WorkerStatusCode{
			Code:  item.Metric["code"],
			Value: valueNumber(item.Value),
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
	rowsWritten, err := c.queryRange(`sum(increase(nanoflare_db_rows_written_total{` + selector + `}[5m]))`)
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
		Available:    true,
		Queries:      resultPoints(queries),
		ReadQueries:  resultPoints(readQueries),
		WriteQueries: resultPoints(writeQueries),
		RowsRead:     resultPoints(rowsRead),
		RowsWritten:  resultPoints(rowsWritten),
		StorageBytes: resultPoints(storageBytes),
		TableCount:   resultPoints(tableCount),
		P50LatencyMS: resultPoints(p50Latency),
		P95LatencyMS: resultPoints(p95Latency),
		P99LatencyMS: resultPoints(p99Latency),
	}, nil
}

func (c *Client) query(query string) ([]prometheusResult, error) {
	return c.get("/api/v1/query", url.Values{"query": []string{query}})
}

func (c *Client) queryRange(query string) ([]prometheusResult, error) {
	end := time.Now().Unix()
	return c.get("/api/v1/query_range", url.Values{
		"query": []string{query},
		"start": []string{strconv.FormatInt(end-24*60*60, 10)},
		"end":   []string{strconv.FormatInt(end, 10)},
		"step":  []string{"300"},
	})
}

func (c *Client) get(path string, values url.Values) ([]prometheusResult, error) {
	requestURL := c.BaseURL + path + "?" + values.Encode()
	response, err := c.Client.Get(requestURL)
	if err != nil {
		return nil, fmt.Errorf("query prometheus: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query prometheus: status %s", response.Status)
	}
	var payload prometheusResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode prometheus response: %w", err)
	}
	if payload.Status != "success" {
		return nil, fmt.Errorf("query prometheus: response status %q", payload.Status)
	}
	return payload.Data.Result, nil
}

func resultNumber(result []prometheusResult) float64 {
	if len(result) == 0 {
		return 0
	}
	return valueNumber(result[0].Value)
}

func resultValues(result []prometheusResult) []float64 {
	if len(result) == 0 {
		return []float64{}
	}
	values := make([]float64, 0, len(result[0].Values))
	for _, value := range result[0].Values {
		values = append(values, valueNumber(value))
	}
	return values
}

func resultPoints(result []prometheusResult) []nanoflare.MetricPoint {
	if len(result) == 0 {
		return []nanoflare.MetricPoint{}
	}
	points := make([]nanoflare.MetricPoint, 0, len(result[0].Values))
	for _, value := range result[0].Values {
		points = append(points, nanoflare.MetricPoint{
			Timestamp: valueTimestamp(value),
			Value:     valueNumber(value),
		})
	}
	return points
}

func valueTimestamp(value []any) time.Time {
	if len(value) == 0 {
		return time.Time{}
	}
	switch raw := value[0].(type) {
	case float64:
		seconds := int64(raw)
		return time.Unix(seconds, int64((raw-float64(seconds))*1_000_000_000)).UTC()
	case string:
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return time.Time{}
		}
		seconds := int64(parsed)
		return time.Unix(seconds, int64((parsed-float64(seconds))*1_000_000_000)).UTC()
	default:
		return time.Time{}
	}
}

func valueNumber(value []any) float64 {
	if len(value) < 2 {
		return 0
	}
	raw, ok := value[1].(string)
	if !ok {
		return 0
	}
	result, _ := strconv.ParseFloat(raw, 64)
	return result
}
