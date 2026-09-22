package main

import "testing"

func TestCalculateJobProgress(t *testing.T) {
	cases := []struct {
		name                 string
		step, steps          int
		current, total       int
		want                 int
	}{
		{"start", 1, 7, 0, 0, 0},
		{"halfway through third stage", 3, 7, 8, 16, 36},
		{"last stage started", 7, 7, 0, 0, 86},
		{"clamps current", 2, 5, 12, 10, 40},
	}
	for _, tc := range cases {
		if got := calculateJobProgress(tc.step, tc.steps, tc.current, tc.total); got != tc.want {
			t.Fatalf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}

func TestInitialJobProgressPlans(t *testing.T) {
	cases := map[string]int{
		"repair":   7,
		"optimize": 5,
		"run":      3,
		"maintain": 6,
	}
	for kind, steps := range cases {
		progress := initialJobProgress(kind)
		if progress.Step != 1 || progress.Steps != steps || progress.Percent != 0 || progress.Stage == "" {
			t.Fatalf("%s initial progress = %#v", kind, progress)
		}
	}
}

func TestCompleteJobProgressReaches100(t *testing.T) {
	a := &App{state: RuntimeState{
		Running:  true,
		Progress: initialJobProgress("repair"),
	}}
	a.completeJobProgress("完成", "Repair 完成")
	if a.state.Progress.Percent != 100 || a.state.Progress.Stage != "完成" {
		t.Fatalf("unexpected completed progress: %#v", a.state.Progress)
	}
}
