// Package apitypes defines the public request/response types for the HaaS API.
// These can be imported by Go client SDKs.
package apitypes

import "time"

type CreateEnvironmentRequest struct {
	Image         string            `json:"image,omitempty"`
	CPU           float64           `json:"cpu,omitempty"`
	MemoryMB      int64             `json:"memory_mb,omitempty"`
	DiskMB        int64             `json:"disk_mb,omitempty"`
	NetworkPolicy string            `json:"network_policy,omitempty"`
	EnvVars       map[string]string `json:"env_vars,omitempty"`
	SnapshotID    string            `json:"snapshot_id,omitempty"`
}

type CreateEnvironmentResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Image  string `json:"image"`
}

type ExecRequest struct {
	Command        []string `json:"command"`
	WorkingDir     string   `json:"working_dir,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
	CaptureOutput  bool     `json:"capture_output,omitempty"`
}

type ExecEvent struct {
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type FileInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	ModTime string `json:"mod_time"`
}

type ErrorResponse struct {
	Error  string `json:"error"`
	Code   int    `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// EnvironmentSpec describes the resource allocation for a container.
type EnvironmentSpec struct {
	Image         string            `json:"image"`
	CPU           float64           `json:"cpu"`
	MemoryMB      int64             `json:"memory_mb"`
	DiskMB        int64             `json:"disk_mb"`
	NetworkPolicy string            `json:"network_policy"`
	EnvVars       map[string]string `json:"env_vars,omitempty"`
}

// Environment is the full environment resource returned by GET /v1/environments and GET /v1/environments/{id}.
type Environment struct {
	ID          string          `json:"id"`
	Spec        EnvironmentSpec `json:"spec"`
	Status      string          `json:"status"`
	ContainerID string          `json:"container_id,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	LastUsedAt  time.Time       `json:"last_used_at"`
	ExpiresAt   time.Time       `json:"expires_at"`
}

// ExecResult holds the collected output from an exec call.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode string
}

// CreateSnapshotRequest is the request body for POST /v1/environments/{id}/snapshots.
type CreateSnapshotRequest struct {
	Label string `json:"label,omitempty"`
}

// Snapshot is the snapshot resource returned by the API.
type Snapshot struct {
	ID            string    `json:"id"`
	EnvironmentID string    `json:"environment_id"`
	ImageID       string    `json:"image_id"`
	Label         string    `json:"label"`
	Size          int64     `json:"size"`
	CreatedAt     time.Time `json:"created_at"`
}
