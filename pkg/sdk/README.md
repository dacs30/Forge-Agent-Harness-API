# HaaS Go SDK

A Go client for the [HaaS](../../README.md) REST API, with built-in tool definitions and a dispatcher for wiring HaaS into an AI agent tool-use loop.

## Installation

```go
import "haas/pkg/sdk"
```

## Quick Start

```go
client := sdk.New("http://localhost:8080", "your-api-key")

env, err := client.CreateEnvironment(ctx, apitypes.CreateEnvironmentRequest{
    Image: "ubuntu:22.04",
})
if err != nil {
    log.Fatal(err)
}
fmt.Println("Created:", env.ID) // env_a1b2c3d4

result, err := client.Exec(ctx, env.ID, apitypes.ExecRequest{
    Command: []string{"bash", "-c", "echo hello world"},
})
fmt.Println(result.Stdout) // hello world

client.DestroyEnvironment(ctx, env.ID)
```

---

## Client

### Constructor

```go
client := sdk.New(baseURL, apiKey, ...opts)
```

| Option | Description |
|---|---|
| `sdk.WithHTTPClient(hc *http.Client)` | Override the default HTTP client (e.g. custom timeouts, transport) |

### Methods

#### Environments

```go
// Create a new container environment.
env, err := client.CreateEnvironment(ctx, apitypes.CreateEnvironmentRequest{
    Image:         "python:3.12",
    CPU:           1.0,           // 0.1–4.0 (default 1.0)
    MemoryMB:      2048,          // 128–8192 (default 2048)
    DiskMB:        4096,          // default 4096
    NetworkPolicy: "none",        // "none" | "egress-limited" | "full"
    EnvVars:       map[string]string{"DEBUG": "1"},
})

// List all active environments.
envs, err := client.ListEnvironments(ctx)

// Get a specific environment.
env, err := client.GetEnvironment(ctx, "env_a1b2c3d4")

// Destroy an environment.
err := client.DestroyEnvironment(ctx, "env_a1b2c3d4")
```

#### Exec

```go
// Run a command and collect all output.
result, err := client.Exec(ctx, envID, apitypes.ExecRequest{
    Command:        []string{"python", "script.py"},
    WorkingDir:     "/app",
    TimeoutSeconds: 60,
})
fmt.Println(result.Stdout)
fmt.Println(result.Stderr)
fmt.Println(result.ExitCode) // "0"

// Run a command and stream the NDJSON response directly.
// Each line is a JSON-encoded apitypes.ExecEvent.
body, err := client.ExecStream(ctx, envID, apitypes.ExecRequest{
    Command: []string{"bash", "-c", "for i in 1 2 3; do echo $i; sleep 1; done"},
})
defer body.Close()
scanner := bufio.NewScanner(body)
for scanner.Scan() {
    var event apitypes.ExecEvent
    json.Unmarshal(scanner.Bytes(), &event)
    fmt.Printf("[%s] %s", event.Stream, event.Data)
}
```

#### Files

```go
// List files at a path.
files, err := client.ListFiles(ctx, envID, "/app")

// Read a file.
content, err := client.ReadFile(ctx, envID, "/app/main.py")
fmt.Println(string(content))

// Write a file (creates parent directories as needed).
err := client.WriteFile(ctx, envID, "/app/main.py", `print("hello")`)
```

---

## Agent Integration

The SDK includes pre-built tool definitions and a dispatcher to integrate HaaS into an AI agent's tool-use loop without boilerplate.

### Tool Definitions

`sdk.Tools()` returns `[]sdk.ToolDefinition` — a slice of JSON Schema tool definitions whose field names match the Anthropic API format. Pass the result directly to your model call.

```go
type ToolDefinition struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    InputSchema json.RawMessage `json:"input_schema"`
}
```

### Dispatcher

`client.Dispatch(ctx, toolName, rawInput)` routes a model's `tool_use` response to the correct client method and returns the text result to send back as a `tool_result`.

```go
result, err := client.Dispatch(ctx, toolName, rawInput)
```

### Full Example with the Anthropic Go SDK

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log"

    anthropic "github.com/anthropics/anthropic-sdk-go"
    "haas/pkg/sdk"
    "haas/pkg/apitypes"
)

func main() {
    ctx := context.Background()

    haas := sdk.New("http://localhost:8080", "your-haas-api-key")
    ai := anthropic.NewClient() // reads ANTHROPIC_API_KEY from env

    // Marshal HaaS tool definitions into the format the API expects.
    var tools []anthropic.ToolParam
    for _, t := range sdk.Tools() {
        tools = append(tools, anthropic.ToolParam{
            Name:        t.Name,
            Description: anthropic.String(t.Description),
            InputSchema: anthropic.ToolInputSchemaParam{
                Properties: mustMarshal(t.InputSchema),
            },
        })
    }

    messages := []anthropic.MessageParam{
        anthropic.NewUserMessage(anthropic.NewTextBlock(
            "Spin up a Python 3.12 container, write a hello world script to /app/main.py, and run it.",
        )),
    }

    // Agentic loop.
    for {
        resp, err := ai.Messages.New(ctx, anthropic.MessageNewParams{
            Model:     anthropic.ModelClaude3_5SonnetLatest,
            MaxTokens: 4096,
            Tools:     tools,
            Messages:  messages,
        })
        if err != nil {
            log.Fatal(err)
        }

        // Append the assistant turn.
        messages = append(messages, resp.ToParam())

        if resp.StopReason == anthropic.StopReasonEndTurn {
            for _, block := range resp.Content {
                if block.Type == anthropic.ContentBlockTypeText {
                    fmt.Println(block.Text)
                }
            }
            break
        }

        // Handle tool calls.
        var toolResults []anthropic.ToolResultBlockParam
        for _, block := range resp.Content {
            if block.Type != anthropic.ContentBlockTypeToolUse {
                continue
            }
            result, err := haas.Dispatch(ctx, block.Name, block.Input)
            if err != nil {
                result = fmt.Sprintf("error: %s", err)
            }
            toolResults = append(toolResults, anthropic.NewToolResultBlock(block.ID, result, err != nil))
        }
        messages = append(messages, anthropic.NewToolResultMessage(toolResults...))
    }
}

func mustMarshal(v any) any {
    b, _ := json.Marshal(v)
    var out any
    json.Unmarshal(b, &out)
    return out
}
```

### Available Tools

| Tool | Description |
|---|---|
| `haas_create_environment` | Spin up a new container |
| `haas_list_environments` | List active environments |
| `haas_get_environment` | Get environment details |
| `haas_destroy_environment` | Destroy an environment |
| `haas_exec` | Run a shell command, returns stdout/stderr/exit code |
| `haas_list_files` | List files at a path |
| `haas_read_file` | Read a file |
| `haas_write_file` | Write a file |

---

## Error Handling

All methods return a Go `error`. API errors include the message from the server:

```go
env, err := client.GetEnvironment(ctx, "env_doesnotexist")
if err != nil {
    // err.Error() → "environment not found"
}
```

`Dispatch` returns an error for unknown tool names and for any underlying API failure. In an agent loop, you may want to pass the error message back to the model as a `tool_result` so it can recover:

```go
result, err := haas.Dispatch(ctx, block.Name, block.Input)
if err != nil {
    result = fmt.Sprintf("error: %s", err)
}
// Always send a tool_result, even on error.
toolResults = append(toolResults, anthropic.NewToolResultBlock(block.ID, result, err != nil))
```
