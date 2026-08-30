package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
)

// DockerSandbox represents a sandbox environment using Docker.
type DockerSandbox struct {
	Image        string
	WorkDir      string
	Env          map[string]string
	Mounts       map[string]string // hostPath -> containerPath
}

// NewDockerSandbox creates a new Docker sandbox.
func NewDockerSandbox(image, workDir string) *DockerSandbox {
	return &DockerSandbox{
		Image:   image,
		WorkDir: workDir,
		Env:     make(map[string]string),
		Mounts:  make(map[string]string),
	}
}

// AddEnv adds an environment variable to the sandbox.
func (s *DockerSandbox) AddEnv(key, value string) {
	s.Env[key] = value
}

// AddMount adds a volume mount to the sandbox.
func (s *DockerSandbox) AddMount(hostPath, containerPath string) {
	s.Mounts[hostPath] = containerPath
}

// Run executes a command inside the Docker sandbox.
func (s *DockerSandbox) Run(ctx context.Context, cmdArgs []string) (string, string, error) {
	args := []string{"run", "--rm"}

	// Add environment variables
	for k, v := range s.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	// Add mounts
	for hostPath, containerPath := range s.Mounts {
		absHostPath, err := filepath.Abs(hostPath)
		if err != nil {
			return "", "", fmt.Errorf("invalid host path %s: %w", hostPath, err)
		}
		args = append(args, "-v", fmt.Sprintf("%s:%s", absHostPath, containerPath))
	}

	// Set working directory
	if s.WorkDir != "" {
		args = append(args, "-w", s.WorkDir)
	}

	// Add image
	args = append(args, s.Image)

	// Add command
	args = append(args, cmdArgs...)

	cmd := exec.CommandContext(ctx, "docker", args...)
	
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}
