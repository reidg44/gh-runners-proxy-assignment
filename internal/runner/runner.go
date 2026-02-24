package runner

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/config"
)

const networkName = "gh-proxy-runners"

// Provisioner manages Docker container lifecycle for GitHub Actions runners.
type Provisioner struct {
	docker    client.APIClient
	image     string
	networkID string
	gatewayIP string
	logger    *slog.Logger
}

// New creates a Provisioner, pulling the runner image and creating the bridge network.
func New(ctx context.Context, imageName string, logger *slog.Logger) (*Provisioner, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("creating Docker client: %w", err)
	}

	p := &Provisioner{
		docker: cli,
		image:  imageName,
		logger: logger,
	}

	if err := p.pullImage(ctx); err != nil {
		return nil, err
	}

	if err := p.ensureNetwork(ctx); err != nil {
		return nil, err
	}

	return p, nil
}

// GatewayIP returns the Docker bridge network gateway IP.
func (p *Provisioner) GatewayIP() string {
	return p.gatewayIP
}

// StartRunner creates and starts a Docker container for a JIT runner.
func (p *Provisioner) StartRunner(ctx context.Context, name string, profile *config.Profile, jitConfig string, proxyURL string) (containerID string, containerIP string, err error) {
	nanoCPUs, err := parseCPUs(profile.CPUs)
	if err != nil {
		return "", "", fmt.Errorf("parsing CPUs %q: %w", profile.CPUs, err)
	}
	memoryBytes, err := parseMemory(profile.Memory)
	if err != nil {
		return "", "", fmt.Errorf("parsing memory %q: %w", profile.Memory, err)
	}

	resp, err := p.docker.ContainerCreate(ctx,
		&container.Config{
			Image: p.image,
			Cmd:   []string{"/home/runner/run.sh"},
			Env: []string{
				"ACTIONS_RUNNER_INPUT_JITCONFIG=" + jitConfig,
				"https_proxy=" + proxyURL,
				"http_proxy=" + proxyURL,
				"HTTPS_PROXY=" + proxyURL,
				"HTTP_PROXY=" + proxyURL,
			},
			User: "runner",
		},
		&container.HostConfig{
			Resources: container.Resources{
				NanoCPUs: nanoCPUs,
				Memory:   memoryBytes,
			},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				networkName: {NetworkID: p.networkID},
			},
		},
		nil,
		name,
	)
	if err != nil {
		return "", "", fmt.Errorf("creating container: %w", err)
	}

	if err := p.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Clean up on failure
		_ = p.docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", "", fmt.Errorf("starting container: %w", err)
	}

	// Get container IP
	inspect, err := p.docker.ContainerInspect(ctx, resp.ID)
	if err != nil {
		return "", "", fmt.Errorf("inspecting container: %w", err)
	}

	ip := ""
	if netSettings, ok := inspect.NetworkSettings.Networks[networkName]; ok {
		ip = netSettings.IPAddress
	}

	p.logger.Info("container started",
		"name", name,
		"container_id", shortID(resp.ID),
		"ip", ip,
		"cpus", profile.CPUs,
		"memory", profile.Memory,
	)

	return resp.ID, ip, nil
}

// StopRunner stops and removes a runner container.
func (p *Provisioner) StopRunner(ctx context.Context, containerID string) error {
	p.logger.Info("stopping container", "container_id", shortID(containerID))

	if err := p.docker.ContainerStop(ctx, containerID, container.StopOptions{}); err != nil {
		p.logger.Warn("failed to stop container, forcing removal", "error", err)
	}

	if err := p.docker.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("removing container %s: %w", shortID(containerID), err)
	}

	return nil
}

// StopAll stops and removes all tracked containers. Used for graceful shutdown.
func (p *Provisioner) StopAll(ctx context.Context, containerIDs []string) {
	for _, id := range containerIDs {
		if err := p.StopRunner(ctx, id); err != nil {
			p.logger.Error("failed to stop container during shutdown", "container_id", shortID(id), "error", err)
		}
	}
}

func (p *Provisioner) pullImage(ctx context.Context) error {
	p.logger.Info("pulling runner image", "image", p.image)
	reader, err := p.docker.ImagePull(ctx, p.image, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pulling image %s: %w", p.image, err)
	}
	defer reader.Close()
	// Consume the output to complete the pull
	_, _ = io.Copy(io.Discard, reader)
	p.logger.Info("image pull complete", "image", p.image)
	return nil
}

func (p *Provisioner) ensureNetwork(ctx context.Context) error {
	// Check if network exists
	networks, err := p.docker.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing networks: %w", err)
	}
	for _, n := range networks {
		if n.Name == networkName {
			p.networkID = n.ID
			// Inspect to get gateway
			inspect, err := p.docker.NetworkInspect(ctx, n.ID, network.InspectOptions{})
			if err != nil {
				return fmt.Errorf("inspecting network: %w", err)
			}
			if len(inspect.IPAM.Config) > 0 {
				p.gatewayIP = inspect.IPAM.Config[0].Gateway
			}
			p.logger.Info("using existing network", "name", networkName, "gateway", p.gatewayIP)
			return nil
		}
	}

	// Create network
	resp, err := p.docker.NetworkCreate(ctx, networkName, network.CreateOptions{
		Driver: "bridge",
	})
	if err != nil {
		return fmt.Errorf("creating network %s: %w", networkName, err)
	}
	p.networkID = resp.ID

	// Inspect to get gateway IP
	inspect, err := p.docker.NetworkInspect(ctx, resp.ID, network.InspectOptions{})
	if err != nil {
		return fmt.Errorf("inspecting new network: %w", err)
	}
	if len(inspect.IPAM.Config) > 0 {
		p.gatewayIP = inspect.IPAM.Config[0].Gateway
	}

	p.logger.Info("created network", "name", networkName, "id", shortID(resp.ID), "gateway", p.gatewayIP)
	return nil
}

// shortID safely truncates a container ID for logging.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// parseCPUs converts a CPU string like "4" to NanoCPUs (4 * 1e9).
func parseCPUs(cpus string) (int64, error) {
	f, err := strconv.ParseFloat(cpus, 64)
	if err != nil {
		return 0, err
	}
	return int64(f * 1e9), nil
}

// parseMemory converts a memory string like "8g" or "512m" to bytes.
func parseMemory(mem string) (int64, error) {
	mem = strings.TrimSpace(mem)
	if len(mem) == 0 {
		return 0, fmt.Errorf("empty memory string")
	}

	suffix := strings.ToLower(mem[len(mem)-1:])
	numStr := mem[:len(mem)-1]
	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory value %q: %w", mem, err)
	}

	switch suffix {
	case "g":
		return int64(num * 1024 * 1024 * 1024), nil
	case "m":
		return int64(num * 1024 * 1024), nil
	case "k":
		return int64(num * 1024), nil
	default:
		// Try parsing entire string as bytes
		b, err := strconv.ParseInt(mem, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("unknown memory suffix %q", suffix)
		}
		return b, nil
	}
}
