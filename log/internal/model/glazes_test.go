package model

import (
	"strings"
	"testing"
)

func TestGlazesIn(t *testing.T) {
	known := []string{"Pink", "Red", "Blue", "Floating Blue", "Randy's Red", "Celadon"}
	cases := map[string]string{
		"Celadon":                            "Celadon",
		"Pink with dabs of Red":              "Pink|Red",
		"pink with a red rim":                "Pink|Red",          // case-insensitive
		"Floating Blue over red":             "Floating Blue|Red", // longest name wins
		"Randy's Red, then more Randy's Red": "Randy's Red",       // no double counting
		"Reddish blush, bluegreen":           "",                  // whole words only
		"(Pink)/Red.":                        "Pink|Red",
		"":                                   "",
	}
	for text, want := range cases {
		if got := strings.Join(GlazesIn(text, known), "|"); got != want {
			t.Errorf("GlazesIn(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestReplaceGlaze(t *testing.T) {
	got := ReplaceGlaze("celdon inside, Celdon rim, Celdonish", "Celdon", "Celadon")
	if got != "Celadon inside, Celadon rim, Celdonish" {
		t.Errorf("ReplaceGlaze = %q", got)
	}
}
