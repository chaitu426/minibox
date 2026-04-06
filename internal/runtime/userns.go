package runtime

import (
	"syscall"

	"github.com/chaitu426/minibox/internal/config"
)

// userNamespaceMappings returns Docker-style rootless UID/GID maps: the entire container ID
// range maps to a single contiguous host range starting at SubUIDBase. Container "root" (0)
// is never mapped to host UID 0, unlike an insecure mapping that would grant real host root.
func userNamespaceMappings() (uid, gid []syscall.SysProcIDMap) {
	base := config.SubUIDBase
	size := config.SubUIDCount
	m := []syscall.SysProcIDMap{
		{ContainerID: 0, HostID: base, Size: size},
	}
	return m, m
}
