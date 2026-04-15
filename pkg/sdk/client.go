// Package sdk provides a Go client for the HaaS REST API.
//
// Usage:
//
//	client := sdk.New("http://localhost:8080", "your-api-key")
//	env, err := client.CreateEnvironment(ctx, apitypes.CreateEnvironmentRequest{
//	    Image: "ubuntu:22.04",
//	})
package sdk

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"haas/pkg/apitypes"
)

// Client is an HTTP client for the HaaS REST API.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client. Useful for custom timeouts or transports.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// New creates a new Client pointing at baseURL and authenticating with apiKey.
func New(baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// CreateEnvironment provisions a new container and returns its ID and status.
func (c *Client) CreateEnvironment(ctx context.Context, req apitypes.CreateEnvironmentRequest) (*apitypes.CreateEnvironmentResponse, error) {
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

// ListEnvironments returns all active environments.
func (c *Client) ListEnvironments(ctx context.Context) ([]*apitypes.Environment, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/environments", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}

	var out []*apitypes.Environment
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// GetEnvironment returns the details of a specific environment.
func (c *Client) GetEnvironment(ctx context.Context, id string) (*apitypes.Environment, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/environments/"+id, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}

	var out apitypes.Environment
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// DestroyEnvironment stops and permanently removes an environment.
func (c *Client) DestroyEnvironment(ctx context.Context, id string) error {
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

// Exec runs a command inside an environment and returns the collected output.
// The NDJSON stream is consumed transparently; use ExecStream for streaming access.
func (c *Client) Exec(ctx context.Context, envID string, req apitypes.ExecRequest) (*apitypes.ExecResult, error) {
	resp, err := c.do(ctx, http.MethodPost, "/v1/environments/"+envID+"/exec", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}

	result := &apitypes.ExecResult{}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var event apitypes.ExecEvent
		if err := json.Unmarshal(line, &event); err != nil {
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

// ExecStream runs a command and returns the raw NDJSON response body.
// The caller is responsible for closing the returned ReadCloser.
// Each line is a JSON-encoded apitypes.ExecEvent.
func (c *Client) ExecStream(ctx context.Context, envID string, req apitypes.ExecRequest) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodPost, "/v1/environments/"+envID+"/exec", req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, readAPIError(resp)
	}

	return resp.Body, nil
}

// ListFiles returns the files and directories at path inside the environment.
func (c *Client) ListFiles(ctx context.Context, envID, path string) ([]apitypes.FileInfo, error) {
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

// ReadFile returns the raw bytes of a file inside the environment.
func (c *Client) ReadFile(ctx context.Context, envID, path string) ([]byte, error) {
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

// WriteFile uploads content to path inside the environment, creating parent directories as needed.
func (c *Client) WriteFile(ctx context.Context, envID, path, content string) error {
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

// CreateSnapshot snapshots a running environment's filesystem and returns the snapshot.
func (c *Client) CreateSnapshot(ctx context.Context, envID string, req apitypes.CreateSnapshotRequest) (*apitypes.Snapshot, error) {
	resp, err := c.do(ctx, http.MethodPost, "/v1/environments/"+envID+"/snapshots", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return nil, readAPIError(resp)
	}

	var out apitypes.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// ListSnapshots returns all snapshots for the authenticated user.
func (c *Client) ListSnapshots(ctx context.Context) ([]*apitypes.Snapshot, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/snapshots", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}

	var out []*apitypes.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// GetSnapshot returns a single snapshot by ID.
func (c *Client) GetSnapshot(ctx context.Context, id string) (*apitypes.Snapshot, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/snapshots/"+id, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}

	var out apitypes.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// DeleteSnapshot removes a snapshot and its underlying Docker image.
func (c *Client) DeleteSnapshot(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/v1/snapshots/"+id, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return readAPIError(resp)
	}
	return nil
}

// RestoreSnapshot creates a new environment from a snapshot.
// The snapshot_id field is set automatically; all other fields follow the same
// rules as CreateEnvironment (CPU/memory/network_policy defaults apply).
func (c *Client) RestoreSnapshot(ctx context.Context, snapshotID string, req apitypes.CreateEnvironmentRequest) (*apitypes.CreateEnvironmentResponse, error) {
	req.SnapshotID = snapshotID
	return c.CreateEnvironment(ctx, req)
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
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
