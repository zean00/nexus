package services

import "testing"

func TestVisibleRuntimeEventSourceSupportsRunDedupe(t *testing.T) {
	cases := []struct {
		source string
		want   bool
	}{
		{source: "ai_agent", want: true},
		{source: "assistant", want: true},
		{source: "human_agent_on_behalf_of_ai_agent", want: true},
		{source: " HUMAN_AGENT_ON_BEHALF_OF_AI_AGENT ", want: true},
		{source: "human_agent", want: false},
		{source: "operator", want: false},
		{source: "", want: false},
	}

	for _, tc := range cases {
		if got := visibleRuntimeEventSourceSupportsRunDedupe(tc.source); got != tc.want {
			t.Fatalf("visibleRuntimeEventSourceSupportsRunDedupe(%q) = %v, want %v", tc.source, got, tc.want)
		}
	}
}
