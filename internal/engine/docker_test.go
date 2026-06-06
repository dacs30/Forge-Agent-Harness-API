package engine

import (
	"testing"

	"haas/internal/domain"
)

func TestContainerConfig_DefaultsToWorkspace(t *testing.T) {
	env := &domain.Environment{
		ID: "env_test",
		Spec: domain.EnvironmentSpec{
			Image: "alpine:latest",
			EnvVars: map[string]string{
				"FOO": "bar",
			},
		},
	}

	cfg := containerConfig(env)

	if cfg.WorkingDir != "/workspace" {
		t.Fatalf("WorkingDir: want /workspace, got %q", cfg.WorkingDir)
	}
	if cfg.Labels["haas.environment.id"] != env.ID {
		t.Fatalf("environment label: want %q, got %q", env.ID, cfg.Labels["haas.environment.id"])
	}
	if len(cfg.Env) != 1 || cfg.Env[0] != "FOO=bar" {
		t.Fatalf("Env: want [FOO=bar], got %#v", cfg.Env)
	}
}
