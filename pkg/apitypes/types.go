// Package apitypes defines the public request/response types for the HaaS API.
// These can be imported by Go client SDKs.
package apitypes

type CreateEnvironmentRequest struct {
	Image         string            `json:"image"`
	CPU           float64           `json:"cpu,omitempty"`
	MemoryMB      int64             `json:"memory_mb,omitempty"`
	DiskMB        int64             `json:"disk_mb,omitempty"`
	NetworkPolicy string            `json:"network_policy,omitempty"`
	EnvVars       map[string]string `json:"env_vars,omitempty"`
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
