package web

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/model"
)

// Step dates on the edit page: each step's date can be corrected, within
// the previous and next steps' dates (and not after today), optionally for
// the project's other pieces that have the same step on the same date.

type stepDateRow struct {
	EventID     int64
	Label       string
	Date        string
	Min, Max    string // "" for no lower limit
	Approximate bool
	Siblings    []int64
}

type stepDates struct {
	PieceID int64
	Rows    []stepDateRow
	Message string // after a save: what changed, or why it couldn't
	Failed  bool
}

func (s *Server) loadStepDates(r *http.Request, pieceID int64, events []db.Event) (stepDates, error) {
	sibs, err := s.Store.StepSiblings(r.Context(), pieceID)
	if err != nil {
		return stepDates{}, err
	}
	sd := stepDates{PieceID: pieceID}
	today := model.Today(s.Now())
	for i, e := range events {
		row := stepDateRow{EventID: e.ID, Label: e.Action.Label(), Date: e.Date, Max: today,
			Approximate: e.Approximate, Siblings: sibs[e.ID]}
		if i > 0 {
			row.Min = events[i-1].Date
		}
		if i+1 < len(events) {
			row.Max = events[i+1].Date
		}
		sd.Rows = append(sd.Rows, row)
	}
	return sd, nil
}

// setStepDate saves one step's date. From the edit page's script it answers
// with the Step dates section (including any error) to swap in place;
// otherwise it goes back to the edit page.
func (s *Server) setStepDate(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	eid, err := pathID(r, "eid")
	if err != nil {
		return err
	}
	date := strings.TrimSpace(r.FormValue("date"))
	var changed []int64
	if date, err = model.ParseDate(date); err != nil {
		err = &db.UserError{Msg: "Choose a date."}
	} else {
		changed, err = s.Store.SetEventDate(r.Context(), id, eid, date, model.Today(s.Now()), r.FormValue("siblings") == "1")
	}
	var ue *db.UserError
	if r.Header.Get("HX-Request") != "true" {
		if err != nil {
			return err
		}
		return redirect(w, r, pieceURL(id)+"/edit")
	}
	if err != nil && !errors.As(err, &ue) {
		return err
	}
	events, err2 := s.Store.PieceEvents(r.Context(), id)
	if err2 != nil {
		return err2
	}
	sd, err2 := s.loadStepDates(r, id, events)
	if err2 != nil {
		return err2
	}
	switch {
	case ue != nil:
		sd.Message, sd.Failed = ue.Msg, true
	case len(changed) > 0:
		var ids []string
		for _, c := range changed {
			ids = append(ids, fmt.Sprintf("#%d", c))
		}
		sd.Message = "Saved, also for " + strings.Join(ids, ", ") + "."
	default:
		sd.Message = "Saved."
	}
	var buf bytes.Buffer
	if err := s.pages["piece_edit"].ExecuteTemplate(&buf, "stepDates", sd); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, err = buf.WriteTo(w)
	return err
}
