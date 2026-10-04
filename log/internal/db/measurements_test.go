package db

import (
	"context"
	"testing"

	"unionpots.nyc/log/internal/model"
)

func TestMeasurementsRecordedWithSteps(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ids := create(t, s, NewPieces{Count: 2, ClayWeight: 1.25, Dims: model.Dims{H: 5, W: 6, D: 6}})

	// Queue #120 for bisque with its size, then #121 without one.
	if err := s.AddEvents(ctx, ids[:1], model.QueuedBisque, "2026-10-05", StepDetails{Dims: model.Dims{H: 4.75, W: 5.5, D: 5.5}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddEvents(ctx, ids[1:], model.QueuedBisque, "2026-10-05", StepDetails{}); err != nil {
		t.Fatal(err)
	}
	m, err := s.GetMeasurements(ctx, 120)
	if err != nil {
		t.Fatal(err)
	}
	if m.ClayWeight != 1.25 || m.Dims[model.Thrown] != (model.Dims{H: 5, W: 6, D: 6}) || m.Dims[model.QueuedBisque] != (model.Dims{H: 4.75, W: 5.5, D: 5.5}) {
		t.Fatalf("#120 measurements = %+v", m)
	}
	m, _ = s.GetMeasurements(ctx, 121)
	if _, ok := m.Dims[model.QueuedBisque]; ok {
		t.Fatalf("#121 wasn't measured at bisque: %+v", m)
	}

	// Dimensions sent with a non-measurable step are ignored.
	if err := s.AddEvents(ctx, []int64{121}, model.Glazed, "2026-10-06", StepDetails{Dims: model.Dims{H: 1}}); err != nil {
		t.Fatal(err)
	}
	if m, _ = s.GetMeasurements(ctx, 121); len(m.Dims) != 1 {
		t.Fatalf("glazed shouldn't record dims: %+v", m)
	}
}

func TestSetMeasurementsReplaces(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := create(t, s, NewPieces{ClayWeight: 2, Dims: model.Dims{H: 3}})[0]
	err := s.SetMeasurements(ctx, id, Measurements{Dims: map[model.Action]model.Dims{model.Finished: {H: 2.5}}})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := s.GetMeasurements(ctx, id)
	if m.ClayWeight != 0 || len(m.Dims) != 1 || m.Dims[model.Finished].H != 2.5 {
		t.Fatalf("after replace: %+v", m)
	}
	if err := s.SetMeasurements(ctx, 999, Measurements{}); err != ErrNotFound {
		t.Fatalf("missing piece: %v", err)
	}
}
