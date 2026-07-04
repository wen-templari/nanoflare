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
	latency, err := c.query(`histogram_quantile(0.95, sum by (le) (rate(traefik_router_request_duration_seconds_bucket{` + selector + `}[5m])))`)
	if err != nil {
		return nanoflare.WorkerTraffic{}, err
	}
	errors, err := c.query(`sum(rate(traefik_router_requests_total{` + selector + `,code=~"5.."}[5m]))`)
	if err != nil {
		return nanoflare.WorkerTraffic{}, err
	}
	traffic, err := c.queryRange(`sum(rate(traefik_router_requests_total{` + selector + `}[5m]))`)
	if err != nil {
		return nanoflare.WorkerTraffic{}, err
	}
	statusCodes, err := c.query(`sum by (code) (rate(traefik_router_requests_total{` + selector + `}[5m]))`)
	if err != nil {
		return nanoflare.WorkerTraffic{}, err
	}
	requestRate := resultNumber(requests)
	result := nanoflare.WorkerTraffic{
		Available:         true,
		RequestsPerSecond: requestRate,
		P95Latency:        resultNumber(latency),
		Traffic:           resultValues(traffic),
		StatusCodes:       make([]nanoflare.WorkerStatusCode, 0, len(statusCodes)),
	}
	if requestRate > 0 {
		result.ErrorRate = resultNumber(errors) / requestRate
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

func (c *Client) query(query string) ([]prometheusResult, error) {
	return c.get("/api/v1/query", url.Values{"query": []string{query}})
}

func (c *Client) queryRange(query string) ([]prometheusResult, error) {
	end := time.Now().Unix()
	return c.get("/api/v1/query_range", url.Values{
		"query": []string{query},
		"start": []string{strconv.FormatInt(end-60*60, 10)},
		"end":   []string{strconv.FormatInt(end, 10)},
		"step":  []string{"120"},
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
