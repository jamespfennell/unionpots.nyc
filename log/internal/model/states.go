// Package model holds the domain vocabulary of the work log: actions, the
// states they lead to, and dates.
//
// A piece's history is a list of dated actions (thrown, trimmed, glazed…).
// Its current state is what it is waiting for, derived from its latest
// action: trimmed → drying, glazed → pending glaze firing, and so on.
package model

import (
	"fmt"
	"time"
	_ "time/tzdata" // the runtime image has no zoneinfo
)

// Action is something that happened to a piece, recorded as a dated event.
type Action string

const (
	Started      Action = "started" // added before throwing, or building over several sessions
	Thrown       Action = "thrown"
	Built        Action = "built"
	Trimmed      Action = "trimmed"
	QueuedBisque Action = "queued_bisque" // dry, measured, into the kiln queue
	Glazed       Action = "glazed"
	Finished     Action = "finished"
	Broken       Action = "broken"
)

// Actions in display order.
var Actions = []Action{Started, Thrown, Built, Trimmed, QueuedBisque, Glazed, Finished, Broken}

// StartActions are how a new piece begins (outside backfill mode).
var StartActions = []Action{Started, Thrown, Built}

func ParseAction(s string) (Action, error) {
	for _, a := range Actions {
		if string(a) == s {
			return a, nil
		}
	}
	return "", fmt.Errorf("unknown action %q", s)
}

func (a Action) Label() string {
	switch a {
	case Started:
		return "Started"
	case Thrown:
		return "Thrown"
	case Built:
		return "Built"
	case Trimmed:
		return "Trimmed"
	case QueuedBisque:
		return "Queued for bisque"
	case Glazed:
		return "Glazed"
	case Finished:
		return "Finished"
	case Broken:
		return "Broken"
	}
	return string(a)
}

// Result is the state a piece is in after this action.
func (a Action) Result() State {
	switch a {
	case Started:
		return StateStarted
	case Thrown:
		return PendingTrimming
	case Built, Trimmed:
		return Drying
	case QueuedBisque:
		return PendingBisque
	case Glazed:
		return PendingGlaze
	case Finished:
		return StateFinished
	case Broken:
		return StateBroken
	}
	return ""
}

// State is what a piece is currently waiting for (or that it's ended).
type State string

const (
	StateStarted    State = "started"
	PendingTrimming State = "pending_trimming"
	Drying          State = "drying"
	PendingBisque   State = "pending_bisque"
	PendingGlaze    State = "pending_glaze"
	StateFinished   State = "finished"
	StateBroken     State = "broken"
)

// States in display order.
var States = []State{StateStarted, PendingTrimming, Drying, PendingBisque, PendingGlaze, StateFinished, StateBroken}

// InProgress are the states shown as sections on the home page.
var InProgress = []State{StateStarted, PendingTrimming, Drying, PendingBisque, PendingGlaze}

func ParseState(s string) (State, error) {
	for _, st := range States {
		if string(st) == s {
			return st, nil
		}
	}
	return "", fmt.Errorf("unknown state %q", s)
}

func (s State) Label() string {
	switch s {
	case StateStarted:
		return "Started"
	case PendingTrimming:
		return "Waiting to be trimmed"
	case Drying:
		return "Drying"
	case PendingBisque:
		return "Waiting to be bisque fired"
	case PendingGlaze:
		return "Waiting to be glaze fired"
	case StateFinished:
		return "Finished"
	case StateBroken:
		return "Broken"
	}
	return string(s)
}

// Next is the action that usually moves a piece on from this state, or ""
// once it has ended. For a started piece it's the more common of its two
// ways on (see NextOptions).
func (s State) Next() Action {
	switch s {
	case StateStarted:
		return Thrown
	case PendingTrimming:
		return Trimmed
	case Drying:
		return QueuedBisque
	case PendingBisque:
		return Glazed
	case PendingGlaze:
		return Finished
	}
	return ""
}

// NextOptions are the ways a piece can usually move on from this state:
// thrown or built for a started piece, otherwise just Next.
func (s State) NextOptions() []Action {
	if s == StateStarted {
		return []Action{Thrown, Built}
	}
	if n := s.Next(); n != "" {
		return []Action{n}
	}
	return nil
}

// Ended reports whether the piece is finished or broken.
func (s State) Ended() bool { return s == StateFinished || s == StateBroken }

// Location is the studio's time zone; event dates are calendar dates here.
var Location = mustLoad("America/New_York")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

const DateLayout = "2006-01-02"

// Today returns the current studio date as YYYY-MM-DD.
func Today(now time.Time) string {
	return now.In(Location).Format(DateLayout)
}

// ParseDate validates a YYYY-MM-DD date.
func ParseDate(s string) (string, error) {
	t, err := time.ParseInLocation(DateLayout, s, Location)
	if err != nil {
		return "", fmt.Errorf("invalid date %q, want YYYY-MM-DD", s)
	}
	return t.Format(DateLayout), nil
}
