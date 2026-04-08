package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"haas/pkg/apitypes"
)

func (s *Server) handleCreateEnvironment(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	image, err := req.RequireString("image")
	if err != nil {
		return mcp.NewToolResultError("image is required"), nil
	}

	createReq := apitypes.CreateEnvironmentRequest{
		Image:         image,
		CPU:           req.GetFloat("cpu", 0),
		MemoryMB:      int64(req.GetFloat("memory_mb", 0)),
		DiskMB:        int64(req.GetFloat("disk_mb", 0)),
		NetworkPolicy: req.GetString("network_policy", ""),
	}

	// env_vars is an object — use GetArguments to extract it
	if raw, ok := req.GetArguments()["env_vars"].(map[string]any); ok {
		createReq.EnvVars = make(map[string]string, len(raw))
		for k, v := range raw {
			createReq.EnvVars[k] = fmt.Sprintf("%v", v)
		}
	}

	env, err := s.client.createEnvironment(ctx, createReq)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create environment: %s", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"Environment created.\nID: %s\nStatus: %s\nImage: %s",
		env.ID, env.Status, env.Image,
	)), nil
}

func (s *Server) handleListEnvironments(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envs, err := s.client.listEnvironments(ctx)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to list environments: %s", err)), nil
	}

	if len(envs) == 0 {
		return mcp.NewToolResultText("No active environments."), nil
	}

	data, err := json.MarshalIndent(envs, "", "  ")
	if err != nil {
		return mcp.NewToolResultError("failed to encode environments"), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleGetEnvironment(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := req.RequireString("environment_id")
	if err != nil {
		return mcp.NewToolResultError("environment_id is required"), nil
	}

	env, err := s.client.getEnvironment(ctx, id)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to get environment: %s", err)), nil
	}

	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return mcp.NewToolResultError("failed to encode environment"), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleDestroyEnvironment(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := req.RequireString("environment_id")
	if err != nil {
		return mcp.NewToolResultError("environment_id is required"), nil
	}

	if err := s.client.destroyEnvironment(ctx, id); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to destroy environment: %s", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Environment %s destroyed.", id)), nil
}

func (s *Server) handleExec(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envID, err := req.RequireString("environment_id")
	if err != nil {
		return mcp.NewToolResultError("environment_id is required"), nil
	}

	// command accepts either a string ("ls -la") or a JSON array (["ls", "-la"])
	var command []string
	switch v := req.GetArguments()["command"].(type) {
	case string:
		command = []string{"sh", "-c", v}
	case []any:
		for _, item := range v {
			command = append(command, fmt.Sprintf("%v", item))
		}
	default:
		return mcp.NewToolResultError("command must be a string or array of strings"), nil
	}

	execReq := apitypes.ExecRequest{
		Command:        command,
		WorkingDir:     req.GetString("working_dir", ""),
		TimeoutSeconds: req.GetInt("timeout_seconds", 30),
	}

	result, err := s.client.exec(ctx, envID, execReq)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("exec failed: %s", err)), nil
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

	return mcp.NewToolResultText(sb.String()), nil
}

func (s *Server) handleListFiles(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envID, err := req.RequireString("environment_id")
	if err != nil {
		return mcp.NewToolResultError("environment_id is required"), nil
	}

	path := req.GetString("path", "/")

	files, err := s.client.listFiles(ctx, envID, path)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to list files: %s", err)), nil
	}

	if len(files) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf("No files found at %s", path)), nil
	}

	data, err := json.MarshalIndent(files, "", "  ")
	if err != nil {
		return mcp.NewToolResultError("failed to encode file list"), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleReadFile(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envID, err := req.RequireString("environment_id")
	if err != nil {
		return mcp.NewToolResultError("environment_id is required"), nil
	}

	path, err := req.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError("path is required"), nil
	}

	content, err := s.client.readFile(ctx, envID, path)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to read file: %s", err)), nil
	}

	return mcp.NewToolResultText(string(content)), nil
}

func (s *Server) handleWriteFile(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envID, err := req.RequireString("environment_id")
	if err != nil {
		return mcp.NewToolResultError("environment_id is required"), nil
	}

	path, err := req.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError("path is required"), nil
	}

	content, err := req.RequireString("content")
	if err != nil {
		return mcp.NewToolResultError("content is required"), nil
	}

	if err := s.client.writeFile(ctx, envID, path, content); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to write file: %s", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("File written to %s", path)), nil
}
