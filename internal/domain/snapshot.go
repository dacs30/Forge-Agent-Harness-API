package domain

import "time"

// Snapshot represents a point-in-time capture of a container's filesystem state.
// It is backed by a local Docker image created via "docker commit".
// Restoring a snapshot creates a new environment from that image; running processes
// are NOT preserved — only the filesystem layer is.
type Snapshot struct {
	ID            string    `json:"id"`
	UserID        string    `json:"user_id"`
	EnvironmentID string    `json:"environment_id"`
	ImageID       string    `json:"image_id"` // Docker image ID (sha256:...)
	Label         string    `json:"label"`
	Size          int64     `json:"size"` // bytes
	CreatedAt     time.Time `json:"created_at"`
}

// ImageRef returns the Docker image reference used to address this snapshot.
func (s *Snapshot) ImageRef() string {
	return "haas-snapshots:" + s.ID
}
