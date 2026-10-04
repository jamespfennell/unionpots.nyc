package model

import (
	"math"
	"testing"
)

func TestParseMeasure(t *testing.T) {
	ok := map[string]float64{
		"":       0,
		"4":      4,
		" 4.5 ":  4.5,
		"4 1/2":  4.5,
		"3/4":    0.75,
		"12 3/8": 12.375,
	}
	for in, want := range ok {
		got, err := ParseMeasure(in)
		if err != nil || math.Abs(got-want) > 1e-9 {
			t.Errorf("ParseMeasure(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"abc", "4.5 1/2", "1/0", "0", "-2", "101", "4 1/", "NaN"} {
		if _, err := ParseMeasure(in); err == nil {
			t.Errorf("ParseMeasure(%q) should fail", in)
		}
	}
}

func TestFormatMeasure(t *testing.T) {
	for in, want := range map[float64]string{0: "", 4: "4", 4.5: "4.5", 12.375: "12.38", 0.75: "0.75"} {
		if got := FormatMeasure(in); got != want {
			t.Errorf("FormatMeasure(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestShrinkage(t *testing.T) {
	if got := Shrinkage(5, 4.4); math.Abs(got-12) > 1e-9 {
		t.Errorf("Shrinkage = %v, want 12", got)
	}
	if Shrinkage(0, 4) != 0 {
		t.Errorf("unknown before should give 0")
	}
}

func TestMeasurable(t *testing.T) {
	if !QueuedBisque.Measurable() || Glazed.Measurable() || Trimmed.Measurable() {
		t.Errorf("Measurable is wrong")
	}
}
