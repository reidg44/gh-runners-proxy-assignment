package runner

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/config"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/dockerutil"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/units"
)

const networkName = "gh-proxy-runners"

// Provisioner manages Docker container lifecycle for GitHub Actions runners.
type Provisioner struct {
	docker       client.APIClient
	image        string
	networkID    string
	gatewayIP    string
	postJobGrace time.Duration
	logger       *slog.Logger
}

// New creates a Provisioner, pulling the runner image and creating the bridge
// network. postJobGrace keeps containers alive after the runner process exits
// so callers (the metrics collector) can still read cgroup files; pass 0 to
// have containers exit as soon as the runner does.
func New(ctx context.Context, imageName string, postJobGrace time.Duration, logger *slog.Logger) (*Provisioner, error) {
	cli, err := dockerutil.NewClient()
	if err != nil {
		return nil, err
	}

	p := &Provisioner{
		docker:       cli,
		image:        imageName,
		postJobGrace: postJobGrace,
		logger:       logger,
	}

	if err := p.ensureImage(ctx); err != nil {
		return nil, err
	}

	if err := p.ensureNetwork(ctx); err != nil {
		return nil, err
	}

	return p, nil
}

// DockerClient exposes the underlying Docker client so other components
// (e.g. the metrics collector) can share the connection.
func (p *Provisioner) DockerClient() client.APIClient {
	return p.docker
}

// GatewayIP returns the address containers should use to reach the host.
// On Docker Desktop (macOS/Windows), the bridge gateway IP doesn't route to
// the host, so we use host.docker.internal instead.
func (p *Provisioner) GatewayIP() string {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return "host.docker.internal"
	}
	return p.gatewayIP
}

// StartRunner creates and starts a Docker container for a JIT runner.
func (p *Provisioner) StartRunner(ctx context.Context, name string, profile *config.Profile, jitConfig string, proxyURL string) (containerID string, containerIP string, err error) {
	nanoCPUs, err := units.ParseCPU(profile.CPUs)
	if err != nil {
		return "", "", fmt.Errorf("parsing CPUs %q: %w", profile.CPUs, err)
	}
	memoryBytes, err := units.ParseMemory(profile.Memory)
	if err != nil {
		return "", "", fmt.Errorf("parsing memory %q: %w", profile.Memory, err)
	}

	cmd := "/home/runner/run.sh"
	if p.postJobGrace > 0 {
		cmd = fmt.Sprintf("%s; sleep %d", cmd, int(p.postJobGrace.Seconds()))
	}

	resp, err := p.docker.ContainerCreate(ctx,
		&container.Config{
			Image: p.image,
			Cmd:   []string{"bash", "-c", cmd},
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
		"container_id", dockerutil.ShortID(resp.ID),
		"ip", ip,
		"cpus", profile.CPUs,
		"memory", profile.Memory,
	)

	return resp.ID, ip, nil
}

// StopRunner stops and removes a runner container.
func (p *Provisioner) StopRunner(ctx context.Context, containerID string) error {
	p.logger.Info("stopping container", "container_id", dockerutil.ShortID(containerID))

	if err := p.docker.ContainerStop(ctx, containerID, container.StopOptions{}); err != nil {
		p.logger.Warn("failed to stop container, forcing removal", "error", err)
	}

	if err := p.docker.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("removing container %s: %w", dockerutil.ShortID(containerID), err)
	}

	return nil
}

// StopAll stops and removes all tracked containers concurrently. Used for
// graceful shutdown, where each sequential stop could otherwise wait the full
// SIGTERM timeout.
func (p *Provisioner) StopAll(ctx context.Context, containerIDs []string) {
	var wg sync.WaitGroup
	for _, id := range containerIDs {
		wg.Go(func() {
			if err := p.StopRunner(ctx, id); err != nil {
				p.logger.Error("failed to stop container during shutdown", "container_id", dockerutil.ShortID(id), "error", err)
			}
		})
	}
	wg.Wait()
}

// ensureImage pulls the runner image only if it isn't already present locally.
// Run `docker pull` manually to pick up a newer tag.
func (p *Provisioner) ensureImage(ctx context.Context) error {
	if _, err := p.docker.ImageInspect(ctx, p.image); err == nil {
		p.logger.Info("runner image present locally", "image", p.image)
		return nil
	}

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

	p.logger.Info("created network", "name", networkName, "id", dockerutil.ShortID(resp.ID), "gateway", p.gatewayIP)
	return nil
}
