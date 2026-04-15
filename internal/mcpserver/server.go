package mcpserver

import (
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Server wraps the MCP server and the HaaS HTTP client.
type Server struct {
	mcp    *server.MCPServer
	client *haasClient
}

// New creates and configures the MCP server with all tools and resources registered.
func New(haasURL, apiKey string) *Server {
	s := &Server{
		mcp:    server.NewMCPServer("haas", "1.0.0"),
		client: newHaasClient(haasURL, apiKey),
	}
	s.registerTools()
	s.registerResources()
	return s
}

// ServeStdio starts the MCP server over stdin/stdout (for Claude Desktop, Cursor, etc.).
func (s *Server) ServeStdio() error {
	return server.ServeStdio(s.mcp)
}

// ServeSSE starts the MCP server over HTTP/SSE on the given address.
// baseURL is the public URL clients will use to reach this server (e.g. the ngrok URL).
// If baseURL is empty it falls back to http://<addr>.
func (s *Server) ServeSSE(addr, baseURL string) error {
	if baseURL == "" {
		baseURL = "http://" + addr
	}
	sse := server.NewSSEServer(s.mcp, server.WithBaseURL(baseURL))
	return sse.Start(addr)
}

// ServeStreamableHTTP starts the MCP server using the Streamable HTTP transport
// (required by VS Code and other modern MCP clients). Listens on addr, serves at /.
// Also registers legacy SSE transport at /sse + /message so that clients using
// the old SSE protocol (Python mcp SDK with sse_client, etc.) still work.
// If apiKeys is non-empty, requests must include a valid Authorization: Bearer <key> header.
func (s *Server) ServeStreamableHTTP(addr string, apiKeys []string) error {
	// Build a valid base URL for the SSE server so it can tell clients where to POST.
	// addr may be ":8091" (no host part) — default to localhost in that case.
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	baseURL := "http://" + host

	// Modern Streamable HTTP transport — POSTs and GET streaming at /.
	streamableHandler := server.NewStreamableHTTPServer(s.mcp, server.WithEndpointPath("/"))

	// Legacy SSE transport — GET /sse for discovery, POST /message for requests.
	// Sharing s.mcp means both transports dispatch to the same registered tools.
	sseServer := server.NewSSEServer(s.mcp, server.WithBaseURL(baseURL))

	mux := http.NewServeMux()
	mux.Handle("/sse", sseServer.SSEHandler())
	mux.Handle("/message", sseServer.MessageHandler())
	mux.Handle("/", streamableHandler) // catch-all — Streamable HTTP

	var handler http.Handler = mux
	if len(apiKeys) > 0 {
		handler = bearerAuthMiddleware(apiKeys, mux)
	}
	return http.ListenAndServe(addr, handler)
}

func bearerAuthMiddleware(apiKeys []string, next http.Handler) http.Handler {
	keySet := make(map[string]struct{}, len(apiKeys))
	for _, k := range apiKeys {
		if k != "" {
			keySet[k] = struct{}{}
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		if token == "" || token == auth { // empty or missing "Bearer " prefix
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if _, ok := keySet[token]; !ok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) registerTools() {
	s.mcp.AddTool(
		mcp.NewTool("haas_create_environment",
			mcp.WithDescription("Create a new sandboxed Docker container environment. Returns an environment ID used in subsequent calls."),
			mcp.WithString("image",
				mcp.Required(),
				mcp.Description("Docker image to use (e.g. 'ubuntu:22.04', 'python:3.12', 'node:20')"),
			),
			mcp.WithNumber("cpu",
				mcp.Description("CPU cores to allocate (0.1–4.0, default 1.0)"),
			),
			mcp.WithNumber("memory_mb",
				mcp.Description("Memory in MB to allocate (128–8192, default 2048)"),
			),
			mcp.WithNumber("disk_mb",
				mcp.Description("Disk space in MB (default 4096)"),
			),
			mcp.WithString("network_policy",
				mcp.Description("Network access: 'none' (isolated), 'egress-limited', or 'full' (default: 'none')"),
			),
			mcp.WithObject("env_vars",
				mcp.Description("Environment variables to set inside the container (key-value map)"),
			),
		),
		s.handleCreateEnvironment,
	)

	s.mcp.AddTool(
		mcp.NewTool("haas_list_environments",
			mcp.WithDescription("List all active container environments."),
		),
		s.handleListEnvironments,
	)

	s.mcp.AddTool(
		mcp.NewTool("haas_get_environment",
			mcp.WithDescription("Get details and current status of a specific environment."),
			mcp.WithString("environment_id",
				mcp.Required(),
				mcp.Description("The environment ID (e.g. 'env_a1b2c3d4')"),
			),
		),
		s.handleGetEnvironment,
	)

	s.mcp.AddTool(
		mcp.NewTool("haas_destroy_environment",
			mcp.WithDescription("Stop and permanently destroy a container environment."),
			mcp.WithString("environment_id",
				mcp.Required(),
				mcp.Description("The environment ID to destroy"),
			),
		),
		s.handleDestroyEnvironment,
	)

	s.mcp.AddTool(
		mcp.NewTool("haas_exec",
			mcp.WithDescription("Execute a command inside a container environment. Returns stdout, stderr, and exit code."),
			mcp.WithString("environment_id",
				mcp.Required(),
				mcp.Description("The environment ID"),
			),
			mcp.WithString("command",
				mcp.Required(),
				mcp.Description("Command to run. Can be a shell string (e.g. 'ls -la /tmp') or a JSON array (e.g. ['python', 'script.py'])"),
			),
			mcp.WithString("working_dir",
				mcp.Description("Working directory inside the container (default: container default)"),
			),
			mcp.WithNumber("timeout_seconds",
				mcp.Description("Max seconds to wait for the command (default: 30)"),
			),
		),
		s.handleExec,
	)

	s.mcp.AddTool(
		mcp.NewTool("haas_list_files",
			mcp.WithDescription("List files and directories at a path inside a container environment."),
			mcp.WithString("environment_id",
				mcp.Required(),
				mcp.Description("The environment ID"),
			),
			mcp.WithString("path",
				mcp.Description("Directory path to list (default: '/')"),
			),
		),
		s.handleListFiles,
	)

	s.mcp.AddTool(
		mcp.NewTool("haas_read_file",
			mcp.WithDescription("Read the contents of a file inside a container environment."),
			mcp.WithString("environment_id",
				mcp.Required(),
				mcp.Description("The environment ID"),
			),
			mcp.WithString("path",
				mcp.Required(),
				mcp.Description("Absolute path to the file (e.g. '/app/main.py')"),
			),
		),
		s.handleReadFile,
	)

	s.mcp.AddTool(
		mcp.NewTool("haas_write_file",
			mcp.WithDescription("Write text content to a file inside a container environment. Creates parent directories as needed."),
			mcp.WithString("environment_id",
				mcp.Required(),
				mcp.Description("The environment ID"),
			),
			mcp.WithString("path",
				mcp.Required(),
				mcp.Description("Absolute path to write (e.g. '/app/main.py')"),
			),
			mcp.WithString("content",
				mcp.Required(),
				mcp.Description("Text content to write to the file"),
			),
		),
		s.handleWriteFile,
	)

	s.mcp.AddTool(
		mcp.NewTool("haas_create_snapshot",
			mcp.WithDescription("Save a snapshot of a running environment's filesystem. Snapshots capture installed packages, files, and configuration — but not running processes. Use haas_restore_snapshot to spin up a new environment from a snapshot."),
			mcp.WithString("environment_id",
				mcp.Required(),
				mcp.Description("The environment ID to snapshot"),
			),
			mcp.WithString("label",
				mcp.Description("Optional human-readable label for the snapshot (e.g. 'before-migration', 'deps-installed')"),
			),
		),
		s.handleCreateSnapshot,
	)

	s.mcp.AddTool(
		mcp.NewTool("haas_list_snapshots",
			mcp.WithDescription("List all saved snapshots."),
		),
		s.handleListSnapshots,
	)

	s.mcp.AddTool(
		mcp.NewTool("haas_restore_snapshot",
			mcp.WithDescription("Create a new environment restored from a snapshot. The new environment starts with the exact filesystem state from when the snapshot was taken."),
			mcp.WithString("snapshot_id",
				mcp.Required(),
				mcp.Description("The snapshot ID to restore from"),
			),
			mcp.WithNumber("cpu",
				mcp.Description("CPU cores to allocate (0.1–4.0, default 1.0)"),
			),
			mcp.WithNumber("memory_mb",
				mcp.Description("Memory in MB to allocate (128–8192, default 2048)"),
			),
			mcp.WithNumber("disk_mb",
				mcp.Description("Disk space in MB (default 4096)"),
			),
			mcp.WithString("network_policy",
				mcp.Description("Network access: 'none' (isolated), 'egress-limited', or 'full' (default: 'none')"),
			),
		),
		s.handleRestoreSnapshot,
	)

	s.mcp.AddTool(
		mcp.NewTool("haas_delete_snapshot",
			mcp.WithDescription("Delete a snapshot and free its storage. This cannot be undone."),
			mcp.WithString("snapshot_id",
				mcp.Required(),
				mcp.Description("The snapshot ID to delete"),
			),
		),
		s.handleDeleteSnapshot,
	)
}

func (s *Server) registerResources() {
	s.mcp.AddResource(
		mcp.NewResource(
			"haas://environments",
			"Active environments",
			mcp.WithResourceDescription("Live list of all active HaaS container environments"),
			mcp.WithMIMEType("application/json"),
		),
		s.handleEnvironmentsResource,
	)

	s.mcp.AddResourceTemplate(
		mcp.NewResourceTemplate(
			"haas://environments/{id}",
			"Environment details",
			mcp.WithTemplateDescription("Details and status of a specific HaaS environment"),
			mcp.WithTemplateMIMEType("application/json"),
		),
		s.handleEnvironmentResource,
	)
}
