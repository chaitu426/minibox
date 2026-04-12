package runtime

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/chaitu426/minibox/internal/config"
)

type ContainerInfo struct {
	ID        string            `json:"id"`
	Image     string            `json:"image"`
	Command   string            `json:"command"`
	PID       int               `json:"pid"`
	Status    string            `json:"status"`
	Health    string            `json:"health,omitempty"` // starting|healthy|unhealthy|none
	CreatedAt time.Time         `json:"created_at"`
	ExitCode  int               `json:"exit_code"`
	Ports     map[string]string `json:"ports,omitempty"` // hostPort -> containerPort
	Name      string            `json:"name,omitempty"`
	Project   string            `json:"project,omitempty"`
	IP        string            `json:"ip,omitempty"`
}

var (
	stateMutex sync.Mutex
	stateFile  = filepath.Join(config.DataRoot, "state.json")
)

func RegisterContainer(info ContainerInfo) error {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	containers := GetAllContainers()
	containers[info.ID] = info
	return saveContainers(containers)
}

func UpdateContainerStatus(id string, status string) error {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	containers := GetAllContainers()
	if c, exists := containers[id]; exists {
		c.Status = status
		containers[id] = c
		return saveContainers(containers)
	}
	return nil
}

func MarkContainerExited(id string, exitCode int) error {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	containers := GetAllContainers()
	if c, exists := containers[id]; exists {
		c.Status = "exited"
		c.Health = "none"
		c.ExitCode = exitCode
		containers[id] = c
		return saveContainers(containers)
	}
	return nil
}

func UpdateContainerHealth(id string, health string) error {
	stateMutex.Lock()
	defer stateMutex.Unlock()
	containers := GetAllContainers()
	if c, exists := containers[id]; exists {
		c.Health = health
		containers[id] = c
		return saveContainers(containers)
	}
	return nil
}

func DeleteContainer(id string) error {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	containers := GetAllContainers()
	delete(containers, id)
	return saveContainers(containers)
}

func GetAllContainers() map[string]ContainerInfo {
	containers := make(map[string]ContainerInfo)
	data, err := os.ReadFile(stateFile)
	if err == nil {
		if config.EncryptionKey != "" && len(data) > 0 {
			decrypted, err := decryptData(data, config.EncryptionKey)
			if err == nil {
				json.Unmarshal(decrypted, &containers)
			} else {
				// Fallback to unencrypted
				json.Unmarshal(data, &containers)
			}
		} else {
			json.Unmarshal(data, &containers)
		}
	}

	// Live check for running containers
	changed := false
	for id, c := range containers {
		if c.Status == "running" && c.PID > 0 {
			// Check if process exists (Signal 0)
			process, err := os.FindProcess(c.PID)
			if err != nil {
				c.Status = "exited"
				c.ExitCode = -1 // Unknown exit code
				containers[id] = c
				changed = true
				continue
			}

			err = process.Signal(syscall.Signal(0))
			if err != nil {
				c.Status = "exited"
				c.ExitCode = -1
				containers[id] = c
				changed = true
			}
		}
	}

	if changed {
		// Update state asynchronously so it doesn't block.
		go saveContainers(containers)
	}

	return containers
}

func saveContainers(containers map[string]ContainerInfo) error {
	os.MkdirAll(config.DataRoot, 0755)
	data, _ := json.MarshalIndent(containers, "", "  ")

	if config.EncryptionKey != "" {
		encrypted, err := encryptData(data, config.EncryptionKey)
		if err == nil {
			return os.WriteFile(stateFile, encrypted, 0644)
		}
		// If encryption fails, fallback to standard error
		return err
	}

	return os.WriteFile(stateFile, data, 0644)
}

func encryptData(data []byte, keyHex string) ([]byte, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("invalid encryption key (must be 64 char hex)")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return gcm.Seal(nonce, nonce, data, nil), nil
}

func decryptData(data []byte, keyHex string) ([]byte, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("invalid encryption key (must be 64 char hex)")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}
