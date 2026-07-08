// Package dockerutil holds small Docker helpers shared by the runner,
// scaler, and metrics packages.
package dockerutil

import (
	"fmt"

	"github.com/docker/docker/client"
)

// NewClient creates a Docker client from the environment with API version
// negotiation — the one construction every component uses.
func NewClient() (*client.Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("creating Docker client: %w", err)
	}
	return cli, nil
}

// ShortID safely truncates a container ID to 12 characters for logging.
func ShortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
