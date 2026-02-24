package runner

import (
	"testing"
)

func TestParseCPUs(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		{"1", 1e9, false},
		{"4", 4e9, false},
		{"0.5", 5e8, false},
		{"2.5", 2.5e9, false},
		{"", 0, true},
		{"abc", 0, true},
	}

	for _, tt := range tests {
		got, err := parseCPUs(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseCPUs(%q) error=%v, wantErr=%v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.expected {
			t.Errorf("parseCPUs(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestParseMemory(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		{"8g", 8 * 1024 * 1024 * 1024, false},
		{"2g", 2 * 1024 * 1024 * 1024, false},
		{"512m", 512 * 1024 * 1024, false},
		{"1024k", 1024 * 1024, false},
		{"1073741824", 1073741824, false}, // 1G in bytes
		{"", 0, true},
		{"abc", 0, true},
		{"8x", 0, true},
	}

	for _, tt := range tests {
		got, err := parseMemory(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseMemory(%q) error=%v, wantErr=%v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.expected {
			t.Errorf("parseMemory(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}
