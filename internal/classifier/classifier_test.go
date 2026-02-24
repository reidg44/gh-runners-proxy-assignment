package classifier

import (
	"testing"

	"github.com/reidg44/gh-runners-proxy-assignment/internal/config"
)

func testProfiles() []config.NamedProfile {
	return []config.NamedProfile{
		{
			Name: "high-cpu",
			Profile: &config.Profile{
				CPUs:          "4",
				Memory:        "8g",
				MatchPatterns: []string{"high-cpu*"},
			},
		},
		{
			Name: "low-cpu",
			Profile: &config.Profile{
				CPUs:          "1",
				Memory:        "2g",
				MatchPatterns: []string{"low-cpu*"},
			},
		},
	}
}

func TestClassifyExactMatch(t *testing.T) {
	c := New(testProfiles(), "low-cpu")

	tests := []struct {
		jobName  string
		expected string
	}{
		{"high-cpu", "high-cpu"},
		{"low-cpu-1", "low-cpu"},
		{"low-cpu-7", "low-cpu"},
	}

	for _, tt := range tests {
		got := c.Classify(tt.jobName)
		if got != tt.expected {
			t.Errorf("Classify(%q) = %q, want %q", tt.jobName, got, tt.expected)
		}
	}
}

func TestClassifyGlobPattern(t *testing.T) {
	c := New(testProfiles(), "low-cpu")

	got := c.Classify("high-cpu-extra")
	if got != "high-cpu" {
		t.Errorf("Classify(high-cpu-extra) = %q, want high-cpu", got)
	}
}

func TestClassifyDefaultFallback(t *testing.T) {
	c := New(testProfiles(), "low-cpu")

	got := c.Classify("unknown-job")
	if got != "low-cpu" {
		t.Errorf("Classify(unknown-job) = %q, want low-cpu", got)
	}
}

func TestClassifyEmptyName(t *testing.T) {
	c := New(testProfiles(), "low-cpu")

	got := c.Classify("")
	if got != "low-cpu" {
		t.Errorf("Classify('') = %q, want low-cpu", got)
	}
}

func TestClassifyFirstMatchWins(t *testing.T) {
	profiles := []config.NamedProfile{
		{
			Name: "special",
			Profile: &config.Profile{
				CPUs:          "8",
				Memory:        "16g",
				MatchPatterns: []string{"test*"},
			},
		},
		{
			Name: "general",
			Profile: &config.Profile{
				CPUs:          "1",
				Memory:        "2g",
				MatchPatterns: []string{"test-*"},
			},
		},
	}

	c := New(profiles, "general")
	got := c.Classify("test-job")
	if got != "special" {
		t.Errorf("Classify(test-job) = %q, want special (first match)", got)
	}
}
