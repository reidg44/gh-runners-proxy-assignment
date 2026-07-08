package units

import "testing"

func TestParseCPU(t *testing.T) {
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
		got, err := ParseCPU(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseCPU(%q) error=%v, wantErr=%v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.expected {
			t.Errorf("ParseCPU(%q) = %d, want %d", tt.input, got, tt.expected)
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
		{"1.5g", 1610612736, false},
		{"512m", 512 * 1024 * 1024, false},
		{"1024k", 1024 * 1024, false},
		{"1073741824", 1073741824, false}, // 1G in bytes
		{"", 0, true},
		{"abc", 0, true},
		{"8x", 0, true},
	}

	for _, tt := range tests {
		got, err := ParseMemory(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseMemory(%q) error=%v, wantErr=%v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.expected {
			t.Errorf("ParseMemory(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestFormatCPU(t *testing.T) {
	tests := []struct {
		nanoCPUs float64
		expected string
	}{
		{2e9, "2"},
		{1.5e9, "1.5"},
		{2.25e9, "2.25"},
	}
	for _, tt := range tests {
		if got := FormatCPU(tt.nanoCPUs); got != tt.expected {
			t.Errorf("FormatCPU(%v) = %q, want %q", tt.nanoCPUs, got, tt.expected)
		}
	}
}

func TestFormatMemory(t *testing.T) {
	tests := []struct {
		bytes    float64
		expected string
	}{
		{8 * 1024 * 1024 * 1024, "8g"},
		{512 * 1024 * 1024, "512m"},
		{1.5 * 1024 * 1024 * 1024, "1536m"},
	}
	for _, tt := range tests {
		if got := FormatMemory(tt.bytes); got != tt.expected {
			t.Errorf("FormatMemory(%v) = %q, want %q", tt.bytes, got, tt.expected)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	// Values produced by Format* must be accepted by Parse* — the adjuster
	// formats adjusted values and the runner re-parses them.
	for _, cpu := range []string{"1", "1.5", "2.25", "4"} {
		n, err := ParseCPU(cpu)
		if err != nil {
			t.Fatalf("ParseCPU(%q): %v", cpu, err)
		}
		if got := FormatCPU(float64(n)); got != cpu {
			t.Errorf("round trip CPU %q -> %q", cpu, got)
		}
	}
	for _, mem := range []string{"2g", "512m", "3g"} {
		n, err := ParseMemory(mem)
		if err != nil {
			t.Fatalf("ParseMemory(%q): %v", mem, err)
		}
		if got := FormatMemory(float64(n)); got != mem {
			t.Errorf("round trip memory %q -> %q", mem, got)
		}
	}
}
