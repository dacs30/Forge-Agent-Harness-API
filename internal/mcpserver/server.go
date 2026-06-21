package mcpserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// mcpAPIKeyContextKey is the context key used to carry the authenticated tenant
// API key through the HTTP middleware into tool handlers.
type mcpAPIKeyContextKey struct{}

// Server wraps the MCP server and the HaaS REST URL + valid API keys.
type Server struct {
	mcp     *server.MCPServer
	restURL string
	apiKeys []string // REST API keys; tool handlers pick the right one from context
}

// New creates and configures the MCP server with all tools and resources registered.
// apiKeys must be the same set of keys used for REST API auth so that each tenant's
// MCP requests are proxied to the REST API using their own key.
func New(haasURL string, apiKeys []string) *Server {
	s := &Server{
		mcp:     server.NewMCPServer("haas", "1.0.0"),
		restURL: strings.TrimRight(haasURL, "/"),
		apiKeys: apiKeys,
	}
	s.registerTools()
	s.registerResources()
	return s
}

// clientFromContext returns a haasClient that uses the tenant API key injected
// by bearerAuthMiddleware. If the context key is absent (stdio mode) or the key
// is not in s.apiKeys (standalone proxy where MCP auth keys differ from REST key),
// it falls back to the first configured REST key.
func (s *Server) clientFromContext(ctx context.Context) *haasClient {
	if key, ok := ctx.Value(mcpAPIKeyContextKey{}).(string); ok && key != "" {
		for _, k := range s.apiKeys {
			if k == key {
				return newHaasClient(s.restURL, key)
			}
		}
	}
	if len(s.apiKeys) > 0 {
		return newHaasClient(s.restURL, s.apiKeys[0])
	}
	return newHaasClient(s.restURL, "")
}

// ServeStdio starts the MCP server over stdin/stdout (for Claude Desktop, Cursor, etc.).
func (s *Server) ServeStdio() error {
	return server.ServeStdio(s.mcp)
}

// ServeSSE starts the MCP server over HTTP/SSE on the given address.
// baseURL is the public URL clients will use to reach this server (e.g. the ngrok URL).
// If baseURL is empty it falls back to http://<addr>.
// If apiKeys is non-empty, requests must include a valid Authorization: Bearer <key> header.
func (s *Server) ServeSSE(addr, baseURL string, apiKeys []string) error {
	if baseURL == "" {
		baseURL = "http://" + addr
	}
	sse := server.NewSSEServer(s.mcp, server.WithBaseURL(baseURL))

	mux := http.NewServeMux()
	mux.Handle("/sse", sse.SSEHandler())
	mux.Handle("/message", sse.MessageHandler())

	var handler http.Handler = mux
	if len(apiKeys) > 0 {
		handler = bearerAuthMiddleware(apiKeys, mux)
	}
	return http.ListenAndServe(addr, handler)
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
	type entry struct {
		hash []byte
		raw  string
	}
	entries := make([]entry, 0, len(apiKeys))
	for _, k := range apiKeys {
		if k != "" {
			h := sha256.Sum256([]byte(k))
			entries = append(entries, entry{hash: []byte(hex.EncodeToString(h[:])), raw: k})
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		if token == "" || token == auth { // empty or missing "Bearer " prefix
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		th := sha256.Sum256([]byte(token))
		candidate := []byte(hex.EncodeToString(th[:]))
		var matchedKey string
		for _, e := range entries {
			if subtle.ConstantTimeCompare(e.hash, candidate) == 1 {
				matchedKey = e.raw
				break
			}
		}
		if matchedKey == "" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		// Inject the authenticated raw key so tool handlers can build a per-tenant REST client.
		ctx := context.WithValue(r.Context(), mcpAPIKeyContextKey{}, matchedKey)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// userIDParam is the optional end-user scoping parameter added to every tool.
// When set, the operation is scoped to that end-user within the tenant.
var userIDParam = mcp.WithString("user_id",
	mcp.Description("Optional end-user identifier. When set, operations are scoped to this user so their containers are isolated from other users under the same API key. Omit to operate as the service owner."),
)

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
			userIDParam,
		),
		s.handleCreateEnvironment,
	)

	s.mcp.AddTool(
		mcp.NewTool("haas_list_environments",
			mcp.WithDescription("List active container environments. Returns only the calling user's environments; use haas_list_tenant_environments to see all users' environments."),
			userIDParam,
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
			userIDParam,
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
			userIDParam,
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
			userIDParam,
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
			userIDParam,
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
			userIDParam,
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
			userIDParam,
		),
		s.handleWriteFile,
	)

	s.mcp.AddTool(
		mcp.NewTool("haas_list_installed_skills",
			mcp.WithDescription("List the Agent Skills installed inside a container environment, each with its name and description from SKILL.md. Use this to discover which skills are available, then read a skill's SKILL.md (via haas_read_file) to load its full instructions before using it."),
			mcp.WithString("environment_id",
				mcp.Required(),
				mcp.Description("The environment ID"),
			),
			userIDParam,
		),
		s.handleListInstalledSkills,
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
			userIDParam,
		),
		s.handleCreateSnapshot,
	)

	s.mcp.AddTool(
		mcp.NewTool("haas_list_snapshots",
			mcp.WithDescription("List saved snapshots for the current user. Use haas_list_tenant_snapshots to see all users' snapshots."),
			userIDParam,
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
			userIDParam,
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
			userIDParam,
		),
		s.handleDeleteSnapshot,
	)

	s.mcp.AddTool(
		mcp.NewTool("haas_list_tenant_environments",
			mcp.WithDescription("List all active environments across every end-user under this API key. Useful for service owners to get a full view of all containers regardless of which user created them."),
		),
		s.handleListTenantEnvironments,
	)

	s.mcp.AddTool(
		mcp.NewTool("haas_list_tenant_snapshots",
			mcp.WithDescription("List all snapshots across every end-user under this API key. Useful for service owners to get a full view of all snapshots regardless of which user created them."),
		),
		s.handleListTenantSnapshots,
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
