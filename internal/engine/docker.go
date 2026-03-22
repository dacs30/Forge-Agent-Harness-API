package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"haas/internal/config"
	"haas/internal/domain"
)

type DockerEngine struct {
	client *client.Client
	logger *slog.Logger
	config *config.Config
}

func NewDockerEngine(cfg *config.Config, logger *slog.Logger) (*DockerEngine, error) {
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if cfg.DockerHost != "" {
		opts = append(opts, client.WithHost(cfg.DockerHost))
	}

	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}

	// Verify connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		return nil, fmt.Errorf("docker ping: %w", err)
	}

	return &DockerEngine{
		client: cli,
		logger: logger,
		config: cfg,
	}, nil
}

func (e *DockerEngine) CreateContainer(ctx context.Context, env *domain.Environment) (string, error) {
	// Pull image
	e.logger.Info("pulling image", "image", env.Spec.Image, "env_id", env.ID)
	pullResp, err := e.client.ImagePull(ctx, env.Spec.Image, image.PullOptions{})
	if err != nil {
		return "", fmt.Errorf("image pull: %w", err)
	}
	// Drain the pull response to complete the pull
	io.Copy(io.Discard, pullResp)
	pullResp.Close()

	// Build container config
	envVars := make([]string, 0, len(env.Spec.EnvVars))
	for k, v := range env.Spec.EnvVars {
		envVars = append(envVars, k+"="+v)
	}

	containerCfg := &container.Config{
		Image: env.Spec.Image,
		Env:   envVars,
		Labels: map[string]string{
			"haas.environment.id": env.ID,
			"haas.managed":        "true",
		},
		// Keep container alive with a long-running process
		Cmd: []string{"sleep", "infinity"},
	}

	hostCfg := securityHostConfig(env.Spec)
	hostCfg.NetworkMode = networkMode(env.Spec.NetworkPolicy)

	resp, err := e.client.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, "haas-"+env.ID)
	if err != nil {
		return "", fmt.Errorf("container create: %w", err)
	}

	e.logger.Info("container created", "container_id", resp.ID[:12], "env_id", env.ID)
	return resp.ID, nil
}

func (e *DockerEngine) StartContainer(ctx context.Context, containerID string) error {
	if err := e.client.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("container start: %w", err)
	}
	return nil
}

func (e *DockerEngine) StopContainer(ctx context.Context, containerID string) error {
	timeout := 10
	stopOpts := container.StopOptions{Timeout: &timeout}
	if err := e.client.ContainerStop(ctx, containerID, stopOpts); err != nil {
		e.logger.Warn("container stop error (may already be stopped)", "error", err, "container_id", containerID[:12])
	}

	if err := e.client.ContainerRemove(ctx, containerID, container.RemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	}); err != nil {
		return fmt.Errorf("container remove: %w", err)
	}

	e.logger.Info("container removed", "container_id", containerID[:12])
	return nil
}

// ExecResult holds the exec ID and the attached stream reader.
type ExecResult struct {
	ExecID string
	Reader io.ReadCloser
}

func (e *DockerEngine) Exec(ctx context.Context, containerID string, req domain.ExecRequest) (io.ReadCloser, error) {
	execCfg := container.ExecOptions{
		Cmd:          req.Command,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
	}
	if req.WorkingDir != "" {
		execCfg.WorkingDir = req.WorkingDir
	}

	execResp, err := e.client.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return nil, fmt.Errorf("exec create: %w", err)
	}

	attachResp, err := e.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("exec attach: %w", err)
	}

	// Return a wrapper that exposes the exec ID for exit code retrieval
	return &execReadCloser{
		reader: attachResp.Reader,
		conn:   attachResp.Conn,
		execID: execResp.ID,
	}, nil
}

type execReadCloser struct {
	reader io.Reader
	conn   io.Closer
	execID string
}

func (e *execReadCloser) Read(p []byte) (int, error) {
	return e.reader.Read(p)
}

func (e *execReadCloser) Close() error {
	return e.conn.Close()
}

func (e *execReadCloser) ExecID() string {
	return e.execID
}

func (e *DockerEngine) ExecExitCode(ctx context.Context, execID string) (int, error) {
	inspect, err := e.client.ContainerExecInspect(ctx, execID)
	if err != nil {
		return -1, fmt.Errorf("exec inspect: %w", err)
	}
	return inspect.ExitCode, nil
}

func (e *DockerEngine) ListFiles(ctx context.Context, containerID string, path string) ([]domain.FileInfo, error) {
	if path == "" {
		path = "/"
	}

	execCfg := container.ExecOptions{
		Cmd:          []string{"find", path, "-maxdepth", "1", "-printf", `%f\t%s\t%T@\t%y\n`},
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
	}

	execResp, err := e.client.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return nil, fmt.Errorf("exec create for ls: %w", err)
	}

	attachResp, err := e.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("exec attach for ls: %w", err)
	}
	defer attachResp.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attachResp.Reader); err != nil {
		return nil, fmt.Errorf("reading ls output: %w", err)
	}

	// Check for find command errors - fall back to ls if find not available
	if stderr.Len() > 0 && stdout.Len() == 0 {
		return e.listFilesWithLS(ctx, containerID, path)
	}

	return parseFileList(path, stdout.String()), nil
}

func (e *DockerEngine) listFilesWithLS(ctx context.Context, containerID string, path string) ([]domain.FileInfo, error) {
	execCfg := container.ExecOptions{
		Cmd:          []string{"ls", "-la", path},
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
	}

	execResp, err := e.client.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return nil, fmt.Errorf("exec create for ls fallback: %w", err)
	}

	attachResp, err := e.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("exec attach for ls fallback: %w", err)
	}
	defer attachResp.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attachResp.Reader); err != nil {
		return nil, fmt.Errorf("reading ls fallback output: %w", err)
	}

	return parseLSOutput(path, stdout.String()), nil
}

func parseFileList(basePath, output string) []domain.FileInfo {
	var files []domain.FileInfo
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 4 {
			continue
		}
		name := parts[0]
		if name == "" || name == "." {
			continue
		}

		var size int64
		fmt.Sscanf(parts[1], "%d", &size)

		isDir := parts[3] == "d"

		filePath := basePath
		if !strings.HasSuffix(filePath, "/") {
			filePath += "/"
		}
		filePath += name

		files = append(files, domain.FileInfo{
			Name:    name,
			Path:    filePath,
			Size:    size,
			IsDir:   isDir,
			ModTime: parts[2],
		})
	}
	return files
}

func parseLSOutput(basePath, output string) []domain.FileInfo {
	var files []domain.FileInfo
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" || strings.HasPrefix(line, "total") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		name := strings.Join(fields[8:], " ")
		if name == "." || name == ".." {
			continue
		}

		var size int64
		fmt.Sscanf(fields[4], "%d", &size)

		isDir := fields[0][0] == 'd'

		filePath := basePath
		if !strings.HasSuffix(filePath, "/") {
			filePath += "/"
		}
		filePath += name

		files = append(files, domain.FileInfo{
			Name:    name,
			Path:    filePath,
			Size:    size,
			IsDir:   isDir,
			ModTime: fields[5] + " " + fields[6] + " " + fields[7],
		})
	}
	return files
}

func (e *DockerEngine) ReadFile(ctx context.Context, containerID string, path string) (io.ReadCloser, error) {
	reader, _, err := e.client.CopyFromContainer(ctx, containerID, path)
	if err != nil {
		return nil, fmt.Errorf("copy from container: %w", err)
	}

	// CopyFromContainer returns a tar stream. Extract the single file.
	tr := tar.NewReader(reader)
	_, err = tr.Next()
	if err != nil {
		reader.Close()
		return nil, fmt.Errorf("reading tar header: %w", err)
	}

	return &tarFileReader{
		Reader:     tr,
		closer:     reader,
	}, nil
}

type tarFileReader struct {
	io.Reader
	closer io.Closer
}

func (t *tarFileReader) Close() error {
	return t.closer.Close()
}

func (e *DockerEngine) WriteFile(ctx context.Context, containerID string, path string, content io.Reader) error {
	// Build a tar archive with the single file
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	data, err := io.ReadAll(content)
	if err != nil {
		return fmt.Errorf("reading content: %w", err)
	}

	// Extract directory and filename from path
	dir, fileName := splitPath(path)

	hdr := &tar.Header{
		Name: fileName,
		Mode: 0644,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("writing tar header: %w", err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("writing tar body: %w", err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("closing tar: %w", err)
	}

	if err := e.client.CopyToContainer(ctx, containerID, dir, &buf, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("copy to container: %w", err)
	}

	return nil
}

func splitPath(path string) (dir, file string) {
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return "/", path
	}
	dir = path[:idx]
	if dir == "" {
		dir = "/"
	}
	file = path[idx+1:]
	return
}

// DemuxDockerStream reads Docker's multiplexed stream format and calls the
// handler for each chunk. The Docker stream header is 8 bytes:
// [stream_type(1)][0(3)][size(4 big-endian)]
func DemuxDockerStream(reader io.Reader, handler func(stream string, data []byte) error) error {
	header := make([]byte, 8)
	for {
		_, err := io.ReadFull(reader, header)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		streamType := "stdout"
		if header[0] == 2 {
			streamType = "stderr"
		}

		size := binary.BigEndian.Uint32(header[4:8])
		payload := make([]byte, size)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return err
		}

		if err := handler(streamType, payload); err != nil {
			return err
		}
	}
}
