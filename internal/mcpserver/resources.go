package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) handleEnvironmentsResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	envs, err := s.client.listEnvironments(ctx)
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}

	data, err := json.MarshalIndent(envs, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode environments: %w", err)
	}

	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      "haas://environments",
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}

func (s *Server) handleEnvironmentResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	// URI format: haas://environments/{id}
	id := strings.TrimPrefix(req.Params.URI, "haas://environments/")
	if id == "" {
		return nil, fmt.Errorf("missing environment id in URI")
	}

	env, err := s.client.getEnvironment(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get environment %s: %w", id, err)
	}

	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode environment: %w", err)
	}

	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}
