package nanoflare

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

var ErrTelemetryQuery = errors.New("invalid telemetry query")
var ErrTelemetryQuota = errors.New("telemetry query admission limit reached")

func (s *Service) queryWorkerOutput(ctx context.Context, org, worker string, query WorkerOutputQuery) ([]WorkerOutputLine, error) {
	if _, err := s.appForOrg(org, worker); err != nil {
		return nil, err
	}
	if query.Limit == 0 {
		query.Limit = 500
	}
	if query.Limit < 1 || query.Limit > 1000 || len(query.Text) > 512 {
		return nil, ErrTelemetryQuery
	}
	switch query.Level {
	case "", "debug", "info", "warn", "error":
	default:
		return nil, ErrTelemetryQuery
	}
	now := time.Now().UTC()
	if query.Until.IsZero() {
		query.Until = now
	}
	if query.Since.IsZero() {
		query.Since = query.Until.Add(-24 * time.Hour)
	}
	if query.Until.Before(query.Since) || query.Until.Sub(query.Since) > 48*time.Hour {
		return nil, ErrTelemetryQuery
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case s.outputQueries <- struct{}{}:
		defer func() { <-s.outputQueries }()
	default:
		return nil, ErrTelemetryQuota
	}
	records, err := s.store.ListDeployments()
	if err != nil {
		return nil, err
	}
	owned := map[string]bool{}
	for _, record := range records {
		if record.App.ID == worker {
			owned[record.Deployment.ID] = true
		}
	}
	if query.DeploymentID != "" && !owned[query.DeploymentID] {
		return nil, ErrArtifactNotFound
	}
	if s.output == nil {
		return []WorkerOutputLine{}, nil
	}
	var lines []WorkerOutputLine
	if reader, ok := s.output.(WorkerOutputQuerier); ok {
		lines, err = reader.Query(ctx, worker, query)
	} else {
		lines = s.output.Output(worker)
	}
	if err != nil {
		return nil, err
	}
	// Enforce the authorized route even when a telemetry backend returns an
	// unexpected stream. Client filters can narrow this scope, never enlarge it.
	result := []WorkerOutputLine{}
	for _, line := range lines {
		if line.AppID != worker || !owned[line.DeploymentID] || query.DeploymentID != "" && line.DeploymentID != query.DeploymentID || query.Level != "" && line.Level != query.Level || query.Text != "" && !strings.Contains(line.Message, query.Text) || line.Timestamp.Before(query.Since) || line.Timestamp.After(query.Until) {
			continue
		}
		result = append(result, line)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Timestamp.Before(result[j].Timestamp) })
	if len(result) > query.Limit {
		result = result[len(result)-query.Limit:]
	}
	return result, nil
}
