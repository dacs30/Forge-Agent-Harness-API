package engine

import (
	"fmt"

	"github.com/docker/docker/api/types/container"
	"haas/internal/domain"
)

func securityHostConfig(spec domain.EnvironmentSpec) *container.HostConfig {
	hc := &container.HostConfig{
		Privileged: false,
		CapDrop:    []string{"ALL"},
		SecurityOpt: []string{
			"no-new-privileges",
		},
		Resources: container.Resources{
			NanoCPUs:  int64(spec.CPU * 1e9),
			Memory:    spec.MemoryMB * 1024 * 1024,
			MemorySwap: spec.MemoryMB * 1024 * 1024, // no swap
			PidsLimit:  pidsLimit(256),
		},
	}

	if spec.NetworkPolicy != domain.NetworkNone {
		hc.CapAdd = []string{"NET_BIND_SERVICE"}
	}

	if spec.DiskMB > 0 {
		hc.StorageOpt = map[string]string{
			"size": formatDiskSize(spec.DiskMB),
		}
	}

	return hc
}

func pidsLimit(n int64) *int64 {
	return &n
}

func formatDiskSize(mb int64) string {
	gb := mb / 1024
	if gb > 0 && mb%1024 == 0 {
		return fmt.Sprintf("%dg", gb)
	}
	return fmt.Sprintf("%dm", mb)
}
