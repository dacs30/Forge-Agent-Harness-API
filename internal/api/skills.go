package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"haas/internal/auth"
	"haas/internal/service"
	"haas/internal/store"
)

type SkillsHandler struct {
	service *service.SkillService
}

func NewSkillsHandler(svc *service.SkillService) *SkillsHandler {
	return &SkillsHandler{service: svc}
}

// Register stores (or replaces) a skill for the authenticated user.
// POST /v1/skills?name=<name>  body: gzip-compressed tar of the skill directory
func (h *SkillsHandler) Register(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	tenantID := auth.TenantIDFromContext(r.Context())
	name := r.URL.Query().Get("name")

	skill, err := h.service.RegisterSkill(r.Context(), tenantID, userID, name, r.Body)
	if err != nil {
		writeSkillError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, skill)
}

// List returns all skills for the authenticated user.
// GET /v1/skills
func (h *SkillsHandler) List(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())

	skills, err := h.service.ListSkills(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list skills")
		return
	}
	writeJSON(w, http.StatusOK, skills)
}

// Get returns a single skill by ID.
// GET /v1/skills/{id}
func (h *SkillsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())

	skill, err := h.service.GetSkill(r.Context(), id, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "skill not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get skill")
		return
	}
	writeJSON(w, http.StatusOK, skill)
}

// Delete removes a skill from the user's library.
// DELETE /v1/skills/{id}
func (h *SkillsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())

	if err := h.service.DeleteSkill(r.Context(), id, userID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "skill not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete skill")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListTenant returns all skills owned by the tenant across all end-users.
// GET /v1/tenant/skills
func (h *SkillsHandler) ListTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())

	skills, err := h.service.ListTenantSkills(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list skills")
		return
	}
	writeJSON(w, http.StatusOK, skills)
}

// InstallToEnv extracts a skill archive directly into a running environment
// without registering it in the tenant library.
// POST /v1/environments/{id}/skills?name=<name>
func (h *SkillsHandler) InstallToEnv(w http.ResponseWriter, r *http.Request) {
	envID := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())
	name := r.URL.Query().Get("name")

	if err := h.service.InstallSkillToEnvironment(r.Context(), envID, userID, name, r.Body); err != nil {
		writeSkillError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListInstalled returns the skills currently installed inside a running
// environment, each with its SKILL.md frontmatter, so an external agent can
// discover and load them.
// GET /v1/environments/{id}/skills
func (h *SkillsHandler) ListInstalled(w http.ResponseWriter, r *http.Request) {
	envID := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())

	skills, err := h.service.ListInstalledSkills(r.Context(), envID, userID)
	if err != nil {
		writeSkillError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, skills)
}

func writeSkillError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "environment not found")
	case errors.Is(err, service.ErrEnvironmentNotRunning):
		writeError(w, http.StatusConflict, "environment is not running")
	case errors.Is(err, service.ErrSkillNameRequired):
		writeError(w, http.StatusBadRequest, "name query parameter is required")
	case errors.Is(err, service.ErrInvalidSkillName):
		writeError(w, http.StatusBadRequest, "invalid skill name (allowed: letters, digits, '.', '_', '-')")
	case errors.Is(err, service.ErrSkillMissingManifest):
		writeError(w, http.StatusBadRequest, "skill archive must contain a top-level SKILL.md")
	case errors.Is(err, service.ErrInvalidSkillArchive):
		writeError(w, http.StatusBadRequest, "invalid skill archive: expected a gzip-compressed tar")
	case errors.Is(err, service.ErrSkillTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "skill archive too large")
	case errors.Is(err, service.ErrGetEnvironment):
		writeError(w, http.StatusInternalServerError, "failed to get environment")
	default:
		writeError(w, http.StatusInternalServerError, "failed to process skill")
	}
}
