package pipeline

import "testing"

func TestSmartObject(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"milkyway nested target/date/bucket", "input/MilkyWay/13_05_2026/Sorted_DNG", "MilkyWay"},
		{"date with dashes", "input/Andromeda/2026-05-13/lights", "Andromeda"},
		{"yyyymmdd date folder", "/data/M31/20260513", "M31"},
		{"plain target", "input/M101", "M101"},
		{"trailing slash", "input/MilkyWay/13_05_2026/Sorted_DNG/", "MilkyWay"},
		{"target with spaces", "input/Orion Nebula/Sorted_DNG", "Orion_Nebula"},
		{"all-generic degrades to leaf", "input/Sorted_DNG", "Sorted_DNG"},
		{"target is a year, not skipped", "input/2026/Sorted_DNG", "2026"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := smartObject(tt.in); got != tt.want {
				t.Fatalf("smartObject(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
