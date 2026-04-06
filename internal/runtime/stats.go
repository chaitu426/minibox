package runtime

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chaitu426/mini-docker/internal/security"
)

type ContainerStats struct {
	ID          string  `json:"id"`
	MemoryUsage uint64  `json:"memory_usage"`
	MemoryLimit uint64  `json:"memory_limit"`
	CPUUsage    uint64  `json:"cpu_usage_usec"`
	CPUPercent  float64 `json:"cpu_percent"`
	Pids        uint64  `json:"pids"`
	NetInput    uint64  `json:"net_input"`
	NetOutput   uint64  `json:"net_output"`
	BlockInput  uint64  `json:"block_input"`
	BlockOutput uint64  `json:"block_output"`
}

var (
	statsLock      sync.Mutex
	lastCPUUsage   = make(map[string]uint64)
	lastSampleTime = make(map[string]time.Time)
)

func GetContainerStats(id string) (ContainerStats, error) {
	if !security.ValidContainerID(id) {
		return ContainerStats{}, fmt.Errorf("invalid container id")
	}
	cgPath := filepath.Join("/sys/fs/cgroup/mini-docker", id)
	if _, err := os.Stat(cgPath); os.IsNotExist(err) {
		return ContainerStats{}, fmt.Errorf("container cgroup not found")
	}

	stats := ContainerStats{ID: id}

	// 1. Read Memory Usage
	if memUsageData, err := os.ReadFile(filepath.Join(cgPath, "memory.current")); err == nil {
		stats.MemoryUsage, _ = strconv.ParseUint(strings.TrimSpace(string(memUsageData)), 10, 64)
	}

	// 2. Read Memory Limit
	if memLimitData, err := os.ReadFile(filepath.Join(cgPath, "memory.max")); err == nil {
		limitStr := strings.TrimSpace(string(memLimitData))
		if limitStr == "max" {
			stats.MemoryLimit = 0 // unlimited
		} else {
			stats.MemoryLimit, _ = strconv.ParseUint(limitStr, 10, 64)
		}
	}

	// 3. Read CPU Usage & Calculate %
	if cpuStatData, err := os.Open(filepath.Join(cgPath, "cpu.stat")); err == nil {
		defer cpuStatData.Close()
		scanner := bufio.NewScanner(cpuStatData)
		var currentCPUUsage uint64
		for scanner.Scan() {
			parts := strings.Fields(scanner.Text())
			if len(parts) == 2 && parts[0] == "usage_usec" {
				currentCPUUsage, _ = strconv.ParseUint(parts[1], 10, 64)
				break
			}
		}
		stats.CPUUsage = currentCPUUsage

		statsLock.Lock()
		prevUsage, hasPrev := lastCPUUsage[id]
		prevTime, hasTime := lastSampleTime[id]
		now := time.Now()

		if hasPrev && hasTime {
			duration := now.Sub(prevTime).Microseconds()
			if duration > 0 {
				usageDelta := currentCPUUsage - prevUsage
				stats.CPUPercent = (float64(usageDelta) / float64(duration)) * 100.0
			}
		}

		lastCPUUsage[id] = currentCPUUsage
		lastSampleTime[id] = now
		statsLock.Unlock()
	}

	// 4. Read Pids
	if pidData, err := os.ReadFile(filepath.Join(cgPath, "pids.current")); err == nil {
		stats.Pids, _ = strconv.ParseUint(strings.TrimSpace(string(pidData)), 10, 64)
	}

	// 5. Read Network I/O (via host veth interface)
	vethName := "veth-" + id[:8]
	rxPath := filepath.Join("/sys/class/net", vethName, "statistics/rx_bytes")
	txPath := filepath.Join("/sys/class/net", vethName, "statistics/tx_bytes")
	if rxData, err := os.ReadFile(rxPath); err == nil {
		stats.NetInput, _ = strconv.ParseUint(strings.TrimSpace(string(rxData)), 10, 64)
	}
	if txData, err := os.ReadFile(txPath); err == nil {
		// Note: RX on host veth is Output for container, TX on host veth is Input for container
		// But usually we just report Host-side stats as Input/Output for simplicity
		stats.NetOutput, _ = strconv.ParseUint(strings.TrimSpace(string(txData)), 10, 64)
	}

	// 6. Read Block I/O (io.stat)
	if ioData, err := os.Open(filepath.Join(cgPath, "io.stat")); err == nil {
		defer ioData.Close()
		scanner := bufio.NewScanner(ioData)
		for scanner.Scan() {
			parts := strings.Fields(scanner.Text())
			// Format: 8:0 rbytes=... wbytes=... rios=... wios=...
			for _, part := range parts {
				if strings.HasPrefix(part, "rbytes=") {
					v, _ := strconv.ParseUint(part[7:], 10, 64)
					stats.BlockInput += v
				} else if strings.HasPrefix(part, "wbytes=") {
					v, _ := strconv.ParseUint(part[7:], 10, 64)
					stats.BlockOutput += v
				}
			}
		}
	}

	return stats, nil
}
