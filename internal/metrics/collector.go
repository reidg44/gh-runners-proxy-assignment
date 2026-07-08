package metrics

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/reidg44/gh-runners-proxy-assignment/internal/dockerutil"
)

// Collector reads resource usage metrics from a running Docker container.
type Collector interface {
	Collect(ctx context.Context, containerID string, duration time.Duration) (*JobMetrics, error)
}

// JobMetrics holds the CPU and memory usage observed for a completed job.
type JobMetrics struct {
	CPUUsedNanoCPUs int64
	MemPeakBytes    int64
}

// DockerCollector implements Collector by reading cgroup files via docker exec.
// It attempts cgroup v2 paths first and falls back to cgroup v1 paths.
type DockerCollector struct {
	docker client.APIClient
}

// NewDockerCollector returns a DockerCollector backed by the given Docker client.
func NewDockerCollector(docker client.APIClient) *DockerCollector {
	return &DockerCollector{docker: docker}
}

// Collect reads cgroup CPU and memory metrics from the container identified by
// containerID. duration is the wall-clock window over which CPU usage is averaged.
// Returns an error if neither CPU nor memory metrics could be read.
func (c *DockerCollector) Collect(ctx context.Context, containerID string, duration time.Duration) (*JobMetrics, error) {
	metrics := &JobMetrics{}

	if cpu, err := c.collectCPU(ctx, containerID, duration); err == nil {
		metrics.CPUUsedNanoCPUs = cpu
	}
	if mem, err := c.collectMemory(ctx, containerID); err == nil {
		metrics.MemPeakBytes = mem
	}

	if metrics.CPUUsedNanoCPUs == 0 && metrics.MemPeakBytes == 0 {
		return nil, fmt.Errorf("no cgroup metrics found in container %s", dockerutil.ShortID(containerID))
	}

	return metrics, nil
}

// collectCPU reads average CPU usage, trying the cgroup v2 file first and
// falling back to the v1 path only when the v2 file can't be read.
func (c *DockerCollector) collectCPU(ctx context.Context, containerID string, duration time.Duration) (int64, error) {
	if content, err := c.execRead(ctx, containerID, "/sys/fs/cgroup/cpu.stat"); err == nil {
		usageUsec, err := parseCPUStatUsageUsec(content)
		if err != nil {
			return 0, err
		}
		return usageUsecToNanoCPUs(usageUsec, duration), nil
	}

	content, err := c.execRead(ctx, containerID, "/sys/fs/cgroup/cpu/cpuacct.usage")
	if err != nil {
		return 0, err
	}
	nanos, err := parseCPUAcctUsage(content)
	if err != nil {
		return 0, err
	}
	return cpuAcctNanosToNanoCPUs(nanos, duration), nil
}

// collectMemory reads peak memory usage, trying the cgroup v2 file first and
// falling back to the v1 path only when the v2 file can't be read.
func (c *DockerCollector) collectMemory(ctx context.Context, containerID string) (int64, error) {
	if content, err := c.execRead(ctx, containerID, "/sys/fs/cgroup/memory.peak"); err == nil {
		return parseMemoryPeak(content)
	}

	content, err := c.execRead(ctx, containerID, "/sys/fs/cgroup/memory/memory.max_usage_in_bytes")
	if err != nil {
		return 0, err
	}
	return parseMemoryPeak(content)
}

// execRead runs `cat path` inside the container and returns the output as a string.
func (c *DockerCollector) execRead(ctx context.Context, containerID, path string) (string, error) {
	execCfg := container.ExecOptions{
		Cmd:          []string{"cat", path},
		AttachStdout: true,
		AttachStderr: true,
	}
	execID, err := c.docker.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return "", fmt.Errorf("exec create: %w", err)
	}

	resp, err := c.docker.ContainerExecAttach(ctx, execID.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("exec attach: %w", err)
	}
	defer resp.Close()

	// Demultiplex the Docker exec stream — raw reads include 8-byte binary
	// frame headers that corrupt parsed output.
	var stdout, stderr bytes.Buffer
	_, _ = stdcopy.StdCopy(&stdout, &stderr, resp.Reader)

	inspect, err := c.docker.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return "", fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return "", fmt.Errorf("cat %s exited with code %d: %s", path, inspect.ExitCode, stderr.String())
	}

	return stdout.String(), nil
}

// parseCPUStatUsageUsec extracts the usage_usec value from cgroup v2 cpu.stat content.
func parseCPUStatUsageUsec(content string) (int64, error) {
	for line := range strings.SplitSeq(content, "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[0] == "usage_usec" {
			return strconv.ParseInt(parts[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("usage_usec not found in cpu.stat")
}

// usageUsecToNanoCPUs converts cumulative CPU microseconds to an average nanocpu
// count over the given duration.
func usageUsecToNanoCPUs(usageUsec int64, duration time.Duration) int64 {
	if duration.Seconds() == 0 {
		return 0
	}
	return int64(float64(usageUsec) * 1000 / duration.Seconds())
}

// parseCPUAcctUsage parses the single integer nanosecond value from cgroup v1
// cpuacct.usage.
func parseCPUAcctUsage(content string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(content), 10, 64)
}

// cpuAcctNanosToNanoCPUs converts cumulative CPU nanoseconds to an average
// nanocpu count over the given duration.
func cpuAcctNanosToNanoCPUs(totalNanos int64, duration time.Duration) int64 {
	if duration.Seconds() == 0 {
		return 0
	}
	return int64(float64(totalNanos) / duration.Seconds())
}

// parseMemoryPeak parses the single integer byte value from either cgroup v2
// memory.peak or cgroup v1 memory.max_usage_in_bytes.
func parseMemoryPeak(content string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(content), 10, 64)
}
