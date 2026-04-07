package metrics

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
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

	cpuContent, err := c.execRead(ctx, containerID, "/sys/fs/cgroup/cpu.stat")
	if err == nil {
		usageUsec, parseErr := parseCPUStatUsageUsec(cpuContent)
		if parseErr == nil {
			metrics.CPUUsedNanoCPUs = usageUsecToNanoCPUs(usageUsec, duration)
		}
	} else {
		v1Content, v1Err := c.execRead(ctx, containerID, "/sys/fs/cgroup/cpu/cpuacct.usage")
		if v1Err == nil {
			nanos, parseErr := parseCPUAcctUsage(v1Content)
			if parseErr == nil {
				metrics.CPUUsedNanoCPUs = cpuAcctNanosToNanoCPUs(nanos, duration)
			}
		}
	}

	memContent, err := c.execRead(ctx, containerID, "/sys/fs/cgroup/memory.peak")
	if err == nil {
		peakBytes, parseErr := parseMemoryPeak(memContent)
		if parseErr == nil {
			metrics.MemPeakBytes = peakBytes
		}
	} else {
		v1Content, v1Err := c.execRead(ctx, containerID, "/sys/fs/cgroup/memory/memory.max_usage_in_bytes")
		if v1Err == nil {
			peakBytes, parseErr := parseMemoryPeak(v1Content)
			if parseErr == nil {
				metrics.MemPeakBytes = peakBytes
			}
		}
	}

	if metrics.CPUUsedNanoCPUs == 0 && metrics.MemPeakBytes == 0 {
		return nil, fmt.Errorf("no cgroup metrics found in container %s", containerID[:12])
	}

	return metrics, nil
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

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, resp.Reader)

	inspect, err := c.docker.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return "", fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return "", fmt.Errorf("cat %s exited with code %d", path, inspect.ExitCode)
	}

	return buf.String(), nil
}

// parseCPUStatUsageUsec extracts the usage_usec value from cgroup v2 cpu.stat content.
func parseCPUStatUsageUsec(content string) (int64, error) {
	for _, line := range strings.Split(content, "\n") {
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
