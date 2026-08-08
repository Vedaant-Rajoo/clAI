package main

import "testing"

func TestPickMatchesScenarioOnWordBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		intent string
		want   string
	}{
		{name: "exact", intent: "hang", want: "hang"},
		{name: "case insensitive", intent: "Please HANG now", want: "hang"},
		{name: "hyphenated scenario", intent: "simulate hard-reject, please", want: "hard-reject"},
		{name: "substring in change", intent: "change directory", want: "normal"},
		{name: "longer ascii word", intent: "avoid a hangover", want: "normal"},
		{name: "longer unicode word", intent: "überhang", want: "normal"},
		{name: "suffix after scenario", intent: "hard-rejected", want: "normal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pick(tc.intent).name; got != tc.want {
				t.Fatalf("pick(%q) = %q, want %q", tc.intent, got, tc.want)
			}
		})
	}
}
