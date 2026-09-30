package tui

import (
	"testing"
	"time"
)

func TestShortDuration(t *testing.T) {
	cases := map[time.Duration]string{
		45 * time.Second:              "45s",
		92 * time.Second:              "1m32s",
		2*time.Minute + 5*time.Second: "2m05s",
	}
	for d, want := range cases {
		if got := shortDuration(d); got != want {
			t.Fatalf("shortDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
