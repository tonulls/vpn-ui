package job

import "testing"

func TestCPUAlertTransitionRequiresUpwardCrossing(t *testing.T) {
	tests := []struct {
		name       string
		wasAbove   bool
		usage      float64
		threshold  float64
		wantNotify bool
		wantAbove  bool
	}{
		{name: "below threshold", usage: 79, threshold: 80, wantAbove: false},
		{name: "first crossing", usage: 80, threshold: 80, wantNotify: true, wantAbove: true},
		{name: "still high no repeat", wasAbove: true, usage: 94, threshold: 80, wantAbove: true},
		{name: "drops below rearms", wasAbove: true, usage: 79, threshold: 80, wantAbove: false},
		{name: "crosses again after rearm", usage: 81, threshold: 80, wantNotify: true, wantAbove: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotNotify, gotAbove := cpuAlertTransition(tt.wasAbove, tt.usage, tt.threshold)
			if gotNotify != tt.wantNotify || gotAbove != tt.wantAbove {
				t.Fatalf("cpuAlertTransition(%v, %v, %v) = (%v, %v), want (%v, %v)",
					tt.wasAbove, tt.usage, tt.threshold, gotNotify, gotAbove, tt.wantNotify, tt.wantAbove)
			}
		})
	}
}
