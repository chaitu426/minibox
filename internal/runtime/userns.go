package runtime

import (
	"syscall"

	"github.com/chaitu426/minibox/internal/config"
)

// Rootless UID/GID mapping (subuid/subgid).
func userNamespaceMappings() (uid, gid []syscall.SysProcIDMap) {
	base := config.SubUIDBase
	size := config.SubUIDCount
	m := []syscall.SysProcIDMap{
		{ContainerID: 0, HostID: base, Size: size},
	}
	return m, m
}
