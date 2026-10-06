package web

import (
	"strings"
	"testing"

	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/model"
)

func TestHistoryDates(t *testing.T) {
	ev := func(date string, approx bool) db.Event {
		return db.Event{Action: model.Thrown, Date: date, Approximate: approx}
	}
	got := history([]db.Event{
		ev("2024-12-01", false), ev("2024-12-15", true), ev("2025-01-02", false),
		ev("2025-01-25", true), ev("2025-02-11", true),
	})
	var dates []string
	for _, h := range got {
		dates = append(dates, strings.TrimSpace(h.Date+" "+h.Year))
	}
	want := "Dec 1 2024 | mid Dec | Jan 2 2025 | late Jan | mid Feb"
	if strings.Join(dates, " | ") != want {
		t.Errorf("history dates:\n got %s\nwant %s", strings.Join(dates, " | "), want)
	}
	if got := history([]db.Event{ev("2024-04-03", true)}); got[0].Date != "early Apr" || got[0].Year != "2024" {
		t.Errorf("approximate first step: %q", got[0].Date)
	}
}
