package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"haas/pkg/apitypes"
)

// haasClient is a thin HTTP client for the HaaS REST API.
type haasClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func newHaasClient(baseURL, apiKey string) *haasClient {
	return &haasClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{}, // no global timeout — exec streams can be long; callers use context deadlines
	}
}

func (c *haasClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http %s %s: %w", method, path, err)
	}
	return resp, nil
}

func (c *haasClient) createEnvironment(ctx context.Context, req apitypes.CreateEnvironmentRequest) (*apitypes.CreateEnvironmentResponse, error) {
	resp, err := c.do(ctx, http.MethodPost, "/v1/environments", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return nil, readAPIError(resp)
	}

	var out apitypes.CreateEnvironmentResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// environmentJSON is the full shape returned by GET /v1/environments and GET /v1/environments/{id}.
type environmentJSON struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Image       string `json:"image"`
	CPU         float64 `json:"cpu"`
	MemoryMB    int64  `json:"memory_mb"`
	NetworkPolicy string `json:"network_policy"`
	CreatedAt   string `json:"created_at"`
	ExpiresAt   string `json:"expires_at"`
	LastUsedAt  string `json:"last_used_at"`
}

func (c *haasClient) listEnvironments(ctx context.Context) ([]environmentJSON, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/environments", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}

	var out []environmentJSON
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

func (c *haasClient) getEnvironment(ctx context.Context, id string) (*environmentJSON, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/environments/"+id, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}

	var out environmentJSON
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

func (c *haasClient) destroyEnvironment(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/v1/environments/"+id, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return readAPIError(resp)
	}
	return nil
}

// execResult holds the collected output from a streaming exec call.
type execResult struct {
	Stdout   string
	Stderr   string
	ExitCode string
}

func (c *haasClient) exec(ctx context.Context, envID string, req apitypes.ExecRequest) (*execResult, error) {
	resp, err := c.do(ctx, http.MethodPost, "/v1/environments/"+envID+"/exec", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}

	result := &execResult{}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var event apitypes.ExecEvent
		if err := json.Unmarshal(line, &event); err != nil {
			slog.Warn("skipping malformed exec event", "line", string(line), "error", err)
			continue
		}
		switch event.Stream {
		case "stdout":
			result.Stdout += event.Data
		case "stderr":
			result.Stderr += event.Data
		case "exit":
			result.ExitCode = event.Data
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading exec stream: %w", err)
	}
	return result, nil
}

func (c *haasClient) listFiles(ctx context.Context, envID, path string) ([]apitypes.FileInfo, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/environments/"+envID+"/files?path="+url.QueryEscape(path), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}

	var out []apitypes.FileInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

func (c *haasClient) readFile(ctx context.Context, envID, path string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/environments/"+envID+"/files/content?path="+url.QueryEscape(path), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}

	return io.ReadAll(resp.Body)
}

func (c *haasClient) writeFile(ctx context.Context, envID, path, content string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		c.baseURL+"/v1/environments/"+envID+"/files/content?path="+url.QueryEscape(path),
		strings.NewReader(content),
	)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http PUT /files/content: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return readAPIError(resp)
	}
	return nil
}

func readAPIError(resp *http.Response) error {
	var apiErr apitypes.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiErr); err != nil {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if apiErr.Detail != "" {
		return fmt.Errorf("%s: %s", apiErr.Error, apiErr.Detail)
	}
	return fmt.Errorf("%s", apiErr.Error)
}
