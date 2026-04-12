package domain

import "time"

type NetworkPolicy string

const (
	NetworkNone          NetworkPolicy = "none"
	NetworkEgressLimited NetworkPolicy = "egress-limited"
	NetworkFull          NetworkPolicy = "full"
)

func (n NetworkPolicy) Valid() bool {
	switch n {
	case NetworkNone, NetworkEgressLimited, NetworkFull:
		return true
	}
	return false
}

type EnvironmentStatus string

const (
	StatusCreating  EnvironmentStatus = "creating"
	StatusRunning   EnvironmentStatus = "running"
	StatusStopping  EnvironmentStatus = "stopping"
	StatusStopped   EnvironmentStatus = "stopped"
	StatusDestroyed EnvironmentStatus = "destroyed"
)

type EnvironmentSpec struct {
	Image         string            `json:"image"`
	CPU           float64           `json:"cpu"`
	MemoryMB      int64             `json:"memory_mb"`
	DiskMB        int64             `json:"disk_mb"`
	NetworkPolicy NetworkPolicy     `json:"network_policy"`
	EnvVars       map[string]string `json:"env_vars"`
}

type Environment struct {
	ID          string            `json:"id"`
	UserID      string            `json:"user_id"`
	Spec        EnvironmentSpec   `json:"spec"`
	Status      EnvironmentStatus `json:"status"`
	ContainerID string            `json:"container_id,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	LastUsedAt  time.Time         `json:"last_used_at"`
	ExpiresAt   time.Time         `json:"expires_at"`
}

type ExecRequest struct {
	Command        []string `json:"command"`
	WorkingDir     string   `json:"working_dir"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	CaptureOutput  bool     `json:"capture_output"`
}

type ExecEvent struct {
	Stream string `json:"stream"` // "stdout", "stderr", "exit"
	Data   string `json:"data"`
}

type FileInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	ModTime string `json:"mod_time"`
}

// WSInputMessage is sent from the client to the server over a WebSocket exec session.
type WSInputMessage struct {
	Type string `json:"type"`           // "input" or "resize"
	Data string `json:"data,omitempty"` // raw bytes to write to stdin (type=input)
	Cols uint   `json:"cols,omitempty"` // terminal width  (type=resize)
	Rows uint   `json:"rows,omitempty"` // terminal height (type=resize)
}

// WSOutputMessage is sent from the server to the client over a WebSocket exec session.
type WSOutputMessage struct {
	Stream string `json:"stream"` // "output", "exit", or "error"
	Data   string `json:"data"`
}
