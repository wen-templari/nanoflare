package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

const defaultOutputLimit = 500

// LokiReader reads the structured worker output written by Vector to Loki.
type LokiReader struct {
	baseURL string
	client  *http.Client
}

func NewLokiReader(baseURL string) *LokiReader {
	return &LokiReader{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Output provides the legacy, error-free output reader interface. Callers that
// support filtering use Query directly and receive any Loki error.
func (r *LokiReader) Output(appID string) []nanoflare.WorkerOutputLine {
	lines, err := r.Query(context.Background(), appID, nanoflare.WorkerOutputQuery{Limit: defaultOutputLimit})
	if err != nil {
		return []nanoflare.WorkerOutputLine{}
	}
	return lines
}

func (r *LokiReader) Query(ctx context.Context, appID string, query nanoflare.WorkerOutputQuery) ([]nanoflare.WorkerOutputLine, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = defaultOutputLimit
	}
	selector := `{worker_id=` + strconv.Quote(appID)
	if query.DeploymentID != "" {
		selector += `,deployment_id=` + strconv.Quote(query.DeploymentID)
	}
	if query.Level != "" {
		selector += `,level=` + strconv.Quote(query.Level)
	}
	selector += `}`
	if query.Text != "" {
		selector += ` |= ` + strconv.Quote(query.Text)
	}

	values := url.Values{
		"query":     {selector},
		"direction": {"backward"},
		"limit":     {strconv.Itoa(limit)},
	}
	if !query.Since.IsZero() {
		values.Set("start", strconv.FormatInt(query.Since.UnixNano(), 10))
	}
	if !query.Until.IsZero() {
		values.Set("end", strconv.FormatInt(query.Until.UnixNano(), 10))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/loki/api/v1/query_range?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	response, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query Loki: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("query Loki: unexpected HTTP status %s", response.Status)
	}

	var payload lokiResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Loki response: %w", err)
	}
	if payload.Status != "success" {
		return nil, fmt.Errorf("query Loki: response status %q", payload.Status)
	}

	lines := make([]nanoflare.WorkerOutputLine, 0)
	for _, stream := range payload.Data.Result {
		for _, value := range stream.Values {
			if len(value) != 2 {
				continue
			}
			nanoseconds, err := strconv.ParseInt(value[0], 10, 64)
			if err != nil {
				continue
			}
			lines = append(lines, nanoflare.WorkerOutputLine{
				Timestamp:    time.Unix(0, nanoseconds).UTC(),
				Level:        stream.Stream["level"],
				Message:      value[1],
				AppID:        appID,
				DeploymentID: stream.Stream["deployment_id"],
			})
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Timestamp.Before(lines[j].Timestamp) })
	return lines, nil
}

type lokiResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Stream map[string]string `json:"stream"`
			Values [][]string        `json:"values"`
		} `json:"result"`
	} `json:"data"`
}
