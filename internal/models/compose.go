package models

type ComposeService struct {
	Image       string            `yaml:"image"`
	Command     []string          `yaml:"command,omitempty"`
	Ports       []string          `yaml:"ports,omitempty"`
	Environment []string          `yaml:"environment,omitempty"`
	Volumes     []string          `yaml:"volumes,omitempty"`
	DependsOn   []string          `yaml:"depends_on,omitempty"`
	DBMode      bool              `yaml:"db_mode,omitempty"`
	ShmSize     int               `yaml:"shm_size,omitempty"`
	User        string            `yaml:"user,omitempty"`
	OOMScoreAdj int               `yaml:"oom_score_adj,omitempty"`
	DataPath    string            `yaml:"data,omitempty"`
	Build       string            `yaml:"build,omitempty"`
}

type ComposeConfig struct {
	Name     string                    `yaml:"name,omitempty"`
	Services map[string]ComposeService `yaml:"services"`
}
