package model

import (
	"testing"
	"time"
)

func TestEveryActionHasAResultState(t *testing.T) {
	for _, a := range Actions {
		if _, err := ParseState(string(a.Result())); err != nil {
			t.Errorf("%s: %v", a, err)
		}
	}
}

func TestUsualPathsEndFinished(t *testing.T) {
	for _, start := range StartActions {
		s, steps := start.Result(), 0
		for !s.Ended() {
			s = s.Next().Result()
			if steps++; steps > len(States) {
				t.Fatalf("no path from %s to an end state", start)
			}
		}
		if s != StateFinished {
			t.Errorf("path from %s ends %s", start, s)
		}
	}
	if StateBroken.Next() != "" || StateFinished.Next() != "" {
		t.Errorf("ended states should have no next action")
	}
}

func TestDryingAndPendingBisqueAreDistinctStates(t *testing.T) {
	if Trimmed.Result() != Drying || Built.Result() != Drying {
		t.Errorf("trimmed and built pieces should be drying")
	}
	if QueuedBisque.Result() != PendingBisque {
		t.Errorf("queued pieces should be pending bisque firing")
	}
}

func TestParseAction(t *testing.T) {
	for _, a := range Actions {
		if got, err := ParseAction(string(a)); err != nil || got != a {
			t.Errorf("ParseAction(%q) = %q, %v", a, got, err)
		}
	}
	if _, err := ParseAction("bisqued"); err == nil {
		t.Errorf("expected error for unknown action")
	}
}

func TestTodayUsesStudioTimeZone(t *testing.T) {
	// 02:00 UTC on Oct 4 is still Oct 3 in New York.
	now := time.Date(2026, 10, 4, 2, 0, 0, 0, time.UTC)
	if got := Today(now); got != "2026-10-03" {
		t.Errorf("Today = %s, want 2026-10-03", got)
	}
}

func TestParseDate(t *testing.T) {
	if _, err := ParseDate("2026-02-30"); err == nil {
		t.Errorf("expected error for invalid date")
	}
	if got, err := ParseDate("2026-10-03"); err != nil || got != "2026-10-03" {
		t.Errorf("ParseDate = %q, %v", got, err)
	}
}

func TestStarted(t *testing.T) {
	if Started.Result() != StateStarted || InProgress[0] != StateStarted {
		t.Errorf("started pieces should be in the first home section")
	}
	opts := StateStarted.NextOptions()
	if len(opts) != 2 || opts[0] != Thrown || opts[1] != Built {
		t.Errorf("a started piece moves on by being thrown or built: %v", opts)
	}
	if len(Drying.NextOptions()) != 1 || StateFinished.NextOptions() != nil {
		t.Errorf("other states have one way on, ended ones none")
	}
}
