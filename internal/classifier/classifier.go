package classifier

import (
	"path/filepath"

	"github.com/reidg44/gh-runners-proxy-assignment/internal/config"
)

// Classifier matches job display names to resource profiles.
type Classifier struct {
	profiles       []config.NamedProfile
	defaultProfile string
}

// New creates a Classifier from ordered profiles and a default profile name.
func New(profiles []config.NamedProfile, defaultProfile string) *Classifier {
	return &Classifier{
		profiles:       profiles,
		defaultProfile: defaultProfile,
	}
}

// Classify returns the profile name that matches the given job display name.
// Profiles are checked in order; first match wins. Falls back to the default profile.
func (c *Classifier) Classify(jobDisplayName string) string {
	for _, np := range c.profiles {
		for _, pattern := range np.Profile.MatchPatterns {
			if matched, _ := filepath.Match(pattern, jobDisplayName); matched {
				return np.Name
			}
		}
	}
	return c.defaultProfile
}
