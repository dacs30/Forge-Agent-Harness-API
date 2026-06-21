package domain

import (
	"path"
	"time"
)

// Skill is a reusable Agent Skill registered by a tenant/end-user. It is a
// directory tree (containing a SKILL.md plus optional supporting files) that
// HaaS installs into a container so agents can discover and use it.
//
// The archive bytes themselves are stored separately (see Store.GetSkillArchive)
// to keep listings lightweight.
type Skill struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	UserID    string    `json:"user_id"`
	Name      string    `json:"name"` // unique per user; used as the install directory name
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// InstallDir returns the absolute path the skill should be extracted to inside
// the container, given the configured base skills directory.
func (s *Skill) InstallDir(baseDir string) string {
	return path.Join(baseDir, s.Name)
}

// InstalledSkill describes a skill discovered inside a running container's skills
// directory. It carries the SKILL.md frontmatter (name + description) so an
// external agent can decide whether to load the full skill (progressive
// disclosure) without reading every file itself.
type InstalledSkill struct {
	Name        string `json:"name"`        // frontmatter name; falls back to the directory name
	Description string `json:"description"` // frontmatter description (may be empty)
	Path        string `json:"path"`        // absolute install path inside the container
}
