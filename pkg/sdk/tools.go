package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"haas/pkg/apitypes"
)

// ToolDefinition is a framework-agnostic tool definition with a JSON Schema for its input.
// Field names intentionally match the Anthropic API's tool format so the slice returned
// by Tools() can be marshalled and passed directly.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Tools returns the definitions for all HaaS tools.
// Pass the result directly to your AI framework's tool list, e.g.:
//
//	tools, _ := json.Marshal(sdk.Tools())
func Tools() []ToolDefinition {
	return []ToolDefinition{
		toolCreateEnvironment,
		toolListEnvironments,
		toolGetEnvironment,
		toolDestroyEnvironment,
		toolExec,
		toolListFiles,
		toolReadFile,
		toolWriteFile,
	}
}

// Dispatch executes a tool call by name with the given JSON-encoded input.
// Returns the text result to pass back to the model as a tool result.
//
// Typical usage inside a tool-use loop:
//
//	case "tool_use":
//	    result, err := client.Dispatch(ctx, block.Name, block.Input)
func (c *Client) Dispatch(ctx context.Context, toolName string, rawInput json.RawMessage) (string, error) {
	switch toolName {
	case "haas_create_environment":
		return c.dispatchCreateEnvironment(ctx, rawInput)
	case "haas_list_environments":
		return c.dispatchListEnvironments(ctx)
	case "haas_get_environment":
		return c.dispatchGetEnvironment(ctx, rawInput)
	case "haas_destroy_environment":
		return c.dispatchDestroyEnvironment(ctx, rawInput)
	case "haas_exec":
		return c.dispatchExec(ctx, rawInput)
	case "haas_list_files":
		return c.dispatchListFiles(ctx, rawInput)
	case "haas_read_file":
		return c.dispatchReadFile(ctx, rawInput)
	case "haas_write_file":
		return c.dispatchWriteFile(ctx, rawInput)
	default:
		return "", fmt.Errorf("unknown tool: %s", toolName)
	}
}

// --- dispatch helpers ---------------------------------------------------------

func (c *Client) dispatchCreateEnvironment(ctx context.Context, raw json.RawMessage) (string, error) {
	var input apitypes.CreateEnvironmentRequest
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	env, err := c.CreateEnvironment(ctx, input)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Environment created.\nID: %s\nStatus: %s\nImage: %s", env.ID, env.Status, env.Image), nil
}

func (c *Client) dispatchListEnvironments(ctx context.Context) (string, error) {
	envs, err := c.ListEnvironments(ctx)
	if err != nil {
		return "", err
	}
	if len(envs) == 0 {
		return "No active environments.", nil
	}
	data, err := json.MarshalIndent(envs, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode response: %w", err)
	}
	return string(data), nil
}

func (c *Client) dispatchGetEnvironment(ctx context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		EnvironmentID string `json:"environment_id"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	env, err := c.GetEnvironment(ctx, input.EnvironmentID)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode response: %w", err)
	}
	return string(data), nil
}

func (c *Client) dispatchDestroyEnvironment(ctx context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		EnvironmentID string `json:"environment_id"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	if err := c.DestroyEnvironment(ctx, input.EnvironmentID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Environment %s destroyed.", input.EnvironmentID), nil
}

func (c *Client) dispatchExec(ctx context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		EnvironmentID  string `json:"environment_id"`
		Command        string `json:"command"`
		WorkingDir     string `json:"working_dir"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}

	timeout := input.TimeoutSeconds
	if timeout == 0 {
		timeout = 30
	}

	result, err := c.Exec(ctx, input.EnvironmentID, apitypes.ExecRequest{
		Command:        []string{"sh", "-c", input.Command},
		WorkingDir:     input.WorkingDir,
		TimeoutSeconds: timeout,
	})
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Exit code: %s\n", result.ExitCode))
	sb.WriteString("\n=== stdout ===\n")
	if result.Stdout != "" {
		sb.WriteString(result.Stdout)
	} else {
		sb.WriteString("(empty)\n")
	}
	sb.WriteString("\n=== stderr ===\n")
	if result.Stderr != "" {
		sb.WriteString(result.Stderr)
	} else {
		sb.WriteString("(empty)\n")
	}
	return sb.String(), nil
}

func (c *Client) dispatchListFiles(ctx context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		EnvironmentID string `json:"environment_id"`
		Path          string `json:"path"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	if input.Path == "" {
		input.Path = "/"
	}
	files, err := c.ListFiles(ctx, input.EnvironmentID, input.Path)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return fmt.Sprintf("No files found at %s", input.Path), nil
	}
	data, err := json.MarshalIndent(files, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode response: %w", err)
	}
	return string(data), nil
}

func (c *Client) dispatchReadFile(ctx context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		EnvironmentID string `json:"environment_id"`
		Path          string `json:"path"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	content, err := c.ReadFile(ctx, input.EnvironmentID, input.Path)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (c *Client) dispatchWriteFile(ctx context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		EnvironmentID string `json:"environment_id"`
		Path          string `json:"path"`
		Content       string `json:"content"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	if err := c.WriteFile(ctx, input.EnvironmentID, input.Path, input.Content); err != nil {
		return "", err
	}
	return fmt.Sprintf("File written to %s", input.Path), nil
}

// --- tool definitions ---------------------------------------------------------

// prop is a single JSON Schema property.
type prop struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// schema builds a JSON Schema object from a property map and optional required fields.
func schema(props map[string]prop, required ...string) json.RawMessage {
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	b, _ := json.Marshal(s)
	return b
}

var toolCreateEnvironment = ToolDefinition{
	Name:        "haas_create_environment",
	Description: "Create a new sandboxed Docker container environment. Returns an environment ID used in subsequent calls.",
	InputSchema: schema(map[string]prop{
		"image":          {Type: "string", Description: "Docker image to use (e.g. 'ubuntu:22.04', 'python:3.12', 'node:20')"},
		"cpu":            {Type: "number", Description: "CPU cores to allocate (0.1–4.0, default 1.0)"},
		"memory_mb":      {Type: "number", Description: "Memory in MB (128–8192, default 2048)"},
		"disk_mb":        {Type: "number", Description: "Disk space in MB (default 4096)"},
		"network_policy": {Type: "string", Description: "Network access: 'none' (isolated), 'egress-limited', or 'full' (default: 'none')"},
	}, "image"),
}

var toolListEnvironments = ToolDefinition{
	Name:        "haas_list_environments",
	Description: "List all active container environments.",
	InputSchema: schema(map[string]prop{}),
}

var toolGetEnvironment = ToolDefinition{
	Name:        "haas_get_environment",
	Description: "Get details and current status of a specific environment.",
	InputSchema: schema(map[string]prop{
		"environment_id": {Type: "string", Description: "The environment ID (e.g. 'env_a1b2c3d4')"},
	}, "environment_id"),
}

var toolDestroyEnvironment = ToolDefinition{
	Name:        "haas_destroy_environment",
	Description: "Stop and permanently destroy a container environment.",
	InputSchema: schema(map[string]prop{
		"environment_id": {Type: "string", Description: "The environment ID to destroy"},
	}, "environment_id"),
}

var toolExec = ToolDefinition{
	Name:        "haas_exec",
	Description: "Execute a shell command inside a container environment. Returns stdout, stderr, and exit code.",
	InputSchema: schema(map[string]prop{
		"environment_id":  {Type: "string", Description: "The environment ID"},
		"command":         {Type: "string", Description: "Shell command to run (e.g. 'ls -la /tmp')"},
		"working_dir":     {Type: "string", Description: "Working directory inside the container (default: container default)"},
		"timeout_seconds": {Type: "number", Description: "Max seconds to wait for the command (default: 30)"},
	}, "environment_id", "command"),
}

var toolListFiles = ToolDefinition{
	Name:        "haas_list_files",
	Description: "List files and directories at a path inside a container environment.",
	InputSchema: schema(map[string]prop{
		"environment_id": {Type: "string", Description: "The environment ID"},
		"path":           {Type: "string", Description: "Directory path to list (default: '/')"},
	}, "environment_id"),
}

var toolReadFile = ToolDefinition{
	Name:        "haas_read_file",
	Description: "Read the contents of a file inside a container environment.",
	InputSchema: schema(map[string]prop{
		"environment_id": {Type: "string", Description: "The environment ID"},
		"path":           {Type: "string", Description: "Absolute path to the file (e.g. '/app/main.py')"},
	}, "environment_id", "path"),
}

var toolWriteFile = ToolDefinition{
	Name:        "haas_write_file",
	Description: "Write text content to a file inside a container environment. Creates parent directories as needed.",
	InputSchema: schema(map[string]prop{
		"environment_id": {Type: "string", Description: "The environment ID"},
		"path":           {Type: "string", Description: "Absolute path to write (e.g. '/app/main.py')"},
		"content":        {Type: "string", Description: "Text content to write to the file"},
	}, "environment_id", "path", "content"),
}
