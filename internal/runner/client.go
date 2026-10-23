package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

type TraefikWriter interface {
	WriteTraefik([]nanoflare.ActiveDeployment) error
}

func (c *Client) RuntimeDeploymentLive(ctx context.Context, appID, deploymentID string) (bool, error) {
	return c.RuntimeInstanceLive(ctx, appID, deploymentID, "")
}
func (c *Client) RuntimeInstanceLive(ctx context.Context, appID, deploymentID, instanceID string) (bool, error) {
	body, err := json.Marshal(map[string]string{"app_id": appID, "deployment_id": deploymentID, "instance_id": instanceID})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/runtime-grants/check", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, errors.New("runner authorization unavailable")
	}
	var result struct {
		Live bool `json:"live"`
	}
	err = json.NewDecoder(response.Body).Decode(&result)
	return result.Live, err
}

func (c *Client) Ensure(ctx context.Context, active nanoflare.ActiveDeployment) (runtime.EnsuredWorker, error) {
	if err := ctx.Err(); err != nil {
		return runtime.EnsuredWorker{}, err
	}
	return runtime.EnsuredWorker{URL: c.baseURL + "/v1/workers/" + active.App.ID + "/" + active.Deployment.ID, Token: c.token, Release: func() {}}, nil
}

type Client struct {
	resources atomic.Pointer[resourceObservation]
	baseURL   string
	token     string
	http      *http.Client
	traefik   TraefikWriter
}

func NewClient(baseURL, token string, traefik TraefikWriter) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 15 * time.Second},
		traefik: traefik,
	}
}

// ValidatePublication performs candidate validation without changing routing.
func (c *Client) ValidatePublication(active []nanoflare.ActiveDeployment) error {
	if err := c.request(http.MethodPost, "/v1/publications/validate", prepareRequest(active), nil); err != nil {
		return fmt.Errorf("validate runner publication: %w", err)
	}
	return nil
}

func (c *Client) Write(active []nanoflare.ActiveDeployment) error {
	var prepared PrepareResponse
	input := prepareRequest(active)
	input.Restore = true
	if err := c.request(http.MethodPost, "/v1/generations/prepare", input, &prepared); err != nil {
		return fmt.Errorf("prepare runner generation: %w", err)
	}
	if err := c.traefik.WriteTraefik(prepared.Deployments); err != nil {
		_ = c.request(http.MethodPost, "/v1/generations/"+prepared.Generation+"/abort", nil, nil)
		return fmt.Errorf("write Traefik config: %w", err)
	}
	if err := c.request(http.MethodPost, "/v1/generations/"+prepared.Generation+"/commit", nil, nil); err != nil {
		return fmt.Errorf("commit runner generation: %w", err)
	}
	return nil
}

func (c *Client) request(method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return errors.New(strings.TrimSpace(string(message)))
	}
	if output == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(output)
}

func (c *Client) RuntimeStatuses(ctx context.Context, appID string) ([]nanoflare.RuntimeRevisionStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/runtime-status?app_id="+url.QueryEscape(appID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("runner runtime status: HTTP %d", response.StatusCode)
	}
	var statuses []nanoflare.RuntimeRevisionStatus
	err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&statuses)
	return statuses, err
}

func (c *Client) ValidateWorkerPublication(active []nanoflare.ActiveDeployment, appID string) error {
	input := prepareRequest(active)
	input.StrictAppID = appID
	return c.request(http.MethodPost, "/v1/publications/validate", input, nil)
}
