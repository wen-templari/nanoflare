package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/clas/platform/internal/platform"
)

type TraefikWriter interface {
	WriteTraefik([]platform.ActiveDeployment) error
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
	traefik TraefikWriter
}

func NewClient(baseURL, token string, traefik TraefikWriter) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 15 * time.Second},
		traefik: traefik,
	}
}

func (c *Client) Write(active []platform.ActiveDeployment) error {
	var prepared PrepareResponse
	if err := c.request(http.MethodPost, "/v1/generations/prepare", prepareRequest(active), &prepared); err != nil {
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
