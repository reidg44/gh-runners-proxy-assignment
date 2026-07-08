package config

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration.
type Config struct {
	GitHub         GitHubConfig        `yaml:"github"`
	Runner         RunnerConfig        `yaml:"runner"`
	Profiles       map[string]*Profile `yaml:"profiles"`
	DefaultProfile string              `yaml:"default_profile"`
	Proxy          ProxyConfig         `yaml:"proxy"`
	Adaptive       AdaptiveConfig      `yaml:"adaptive"`

	// OrderedProfiles is populated after loading for deterministic matching.
	OrderedProfiles []NamedProfile `yaml:"-"`
}

type GitHubConfig struct {
	RepositoryURL string `yaml:"repository_url"`
	ScaleSetName  string `yaml:"scale_set_name"`
	RunnerLabel   string `yaml:"runner_label"`
	RunnerGroup   string `yaml:"runner_group"`
}

type RunnerConfig struct {
	Image      string `yaml:"image"`
	MaxRunners int    `yaml:"max_runners"`
	WorkFolder string `yaml:"work_folder"`
}

type Profile struct {
	CPUs          string   `yaml:"cpus"`
	Memory        string   `yaml:"memory"`
	MatchPatterns []string `yaml:"match_patterns"`
	MaxCPUs       string   `yaml:"max_cpus"`
	MaxMemory     string   `yaml:"max_memory"`
}

type ProxyConfig struct {
	ListenAddr string `yaml:"listen_addr"`
}

type AdaptiveConfig struct {
	Enabled            bool    `yaml:"enabled"`
	DBPath             string  `yaml:"db_path"`
	ScaleUpThreshold   float64 `yaml:"scale_up_threshold"`
	ScaleDownThreshold float64 `yaml:"scale_down_threshold"`
	ScaleFactor        float64 `yaml:"scale_factor"`
	HistoryWindow      int     `yaml:"history_window"`
	MaxCPUs            string  `yaml:"max_cpus"`
	MaxMemory          string  `yaml:"max_memory"`
}

// NamedProfile pairs a profile name with its definition for ordered iteration.
type NamedProfile struct {
	Name    string
	Profile *Profile
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	cfg.buildOrderedProfiles()
	return &cfg, nil
}

func (c *Config) validate() error {
	if c.GitHub.RepositoryURL == "" {
		return fmt.Errorf("github.repository_url is required")
	}
	if c.GitHub.ScaleSetName == "" {
		return fmt.Errorf("github.scale_set_name is required")
	}
	if c.GitHub.RunnerLabel == "" {
		return fmt.Errorf("github.runner_label is required")
	}
	if c.Runner.Image == "" {
		return fmt.Errorf("runner.image is required")
	}
	if c.Runner.MaxRunners <= 0 {
		return fmt.Errorf("runner.max_runners must be positive")
	}
	if len(c.Profiles) == 0 {
		return fmt.Errorf("at least one profile is required")
	}
	if c.DefaultProfile == "" {
		return fmt.Errorf("default_profile is required")
	}
	if _, ok := c.Profiles[c.DefaultProfile]; !ok {
		return fmt.Errorf("default_profile %q does not reference an existing profile", c.DefaultProfile)
	}
	for name, p := range c.Profiles {
		if len(p.MatchPatterns) == 0 {
			return fmt.Errorf("profile %q must have at least one match_pattern", name)
		}
		if p.CPUs == "" {
			return fmt.Errorf("profile %q must specify cpus", name)
		}
		if p.Memory == "" {
			return fmt.Errorf("profile %q must specify memory", name)
		}
	}
	if c.Proxy.ListenAddr == "" {
		return fmt.Errorf("proxy.listen_addr is required")
	}
	if os.Getenv("GITHUB_TOKEN") == "" {
		return fmt.Errorf("GITHUB_TOKEN environment variable is required")
	}
	if c.Adaptive.Enabled {
		if c.Adaptive.ScaleUpThreshold <= 0 || c.Adaptive.ScaleUpThreshold > 1 {
			return fmt.Errorf("adaptive.scale_up_threshold must be between 0 and 1")
		}
		if c.Adaptive.ScaleDownThreshold < 0 || c.Adaptive.ScaleDownThreshold >= c.Adaptive.ScaleUpThreshold {
			return fmt.Errorf("adaptive.scale_down_threshold must be >= 0 and less than scale_up_threshold")
		}
		if c.Adaptive.ScaleFactor <= 1 {
			return fmt.Errorf("adaptive.scale_factor must be greater than 1")
		}
		if c.Adaptive.HistoryWindow <= 0 {
			return fmt.Errorf("adaptive.history_window must be positive")
		}
	}
	return nil
}

func (c *Config) buildOrderedProfiles() {
	names := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	c.OrderedProfiles = make([]NamedProfile, 0, len(names))
	for _, name := range names {
		c.OrderedProfiles = append(c.OrderedProfiles, NamedProfile{
			Name:    name,
			Profile: c.Profiles[name],
		})
	}
}
