package model

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Dims are a piece's height, width and depth in inches. Zero means not
// measured.
type Dims struct {
	H float64 `json:"h,omitempty"`
	W float64 `json:"w,omitempty"`
	D float64 `json:"d,omitempty"`
}

func (d Dims) Empty() bool { return d.H == 0 && d.W == 0 && d.D == 0 }

// MeasuredAt are the actions at which a piece can be measured.
var MeasuredAt = []Action{Thrown, Built, QueuedBisque, Finished}

// Measurable reports whether dimensions can be recorded with action.
func (a Action) Measurable() bool {
	for _, m := range MeasuredAt {
		if a == m {
			return true
		}
	}
	return false
}

// maxMeasure bounds inches and pounds alike; anything bigger is a typo.
const maxMeasure = 100

// ParseMeasure reads a measurement typed by hand: "4.5", "4 1/2", "3/4" or
// "" (not measured, returned as 0).
func ParseMeasure(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	bad := fmt.Errorf("%q isn't a measurement; use e.g. 4.5 or 4 1/2", s)
	var v float64
	whole, frac, hasFrac := strings.Cut(s, " ")
	if !hasFrac && strings.Contains(s, "/") {
		whole, frac, hasFrac = "0", s, true
	}
	w, err := strconv.ParseFloat(whole, 64)
	if err != nil {
		return 0, bad
	}
	v = w
	if hasFrac {
		num, den, ok := strings.Cut(strings.TrimSpace(frac), "/")
		n, err1 := strconv.ParseFloat(num, 64)
		d, err2 := strconv.ParseFloat(den, 64)
		if !ok || err1 != nil || err2 != nil || d == 0 || math.Trunc(w) != w {
			return 0, bad
		}
		v += n / d
	}
	if v <= 0 || v > maxMeasure || math.IsNaN(v) {
		return 0, fmt.Errorf("%q is out of range", s)
	}
	return v, nil
}

// FormatMeasure shows a measurement with at most two decimals: 4.5, 0.75, 6.
func FormatMeasure(v float64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

// Shrinkage is how much smaller after is than before, in percent (0 if
// either is unknown).
func Shrinkage(before, after float64) float64 {
	if before == 0 || after == 0 {
		return 0
	}
	return (before - after) / before * 100
}
