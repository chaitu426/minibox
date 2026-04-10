package compose

import (
	"fmt"
	"os"

	"github.com/chaitu426/minibox/internal/models"
	"gopkg.in/yaml.v3"
)

// ParseConfig reads a minibox-compose.yaml file and returns the config.
func ParseConfig(filename string) (*models.ComposeConfig, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var config models.ComposeConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// SortServices returns service names in an order that satisfies depends_on.
func SortServices(config *models.ComposeConfig) ([]string, error) {
	services := config.Services
	var sorted []string
	visited := make(map[string]bool)
	temp := make(map[string]bool)

	var visit func(string) error
	visit = func(name string) error {
		if temp[name] {
			return fmt.Errorf("circular dependency detected at %s", name)
		}
		if !visited[name] {
			temp[name] = true
			svc, ok := services[name]
			if !ok {
				return fmt.Errorf("service %s not found", name)
			}
			for _, dep := range svc.DependsOn {
				if err := visit(dep); err != nil {
					return err
				}
			}
			visited[name] = true
			delete(temp, name)
			sorted = append(sorted, name)
		}
		return nil
	}

	for name := range services {
		if !visited[name] {
			if err := visit(name); err != nil {
				return nil, err
			}
		}
	}

	return sorted, nil
}
