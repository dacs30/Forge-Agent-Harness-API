package engine

import (
	"github.com/docker/docker/api/types/container"
	"haas/internal/domain"
)

func networkMode(policy domain.NetworkPolicy) container.NetworkMode {
	switch policy {
	case domain.NetworkNone:
		return "none"
	case domain.NetworkEgressLimited:
		// MVP: uses bridge. Production should use a custom Docker network with
		// iptables rules restricting egress to specific CIDRs.
		return "bridge"
	case domain.NetworkFull:
		return "bridge"
	default:
		return "none"
	}
}
