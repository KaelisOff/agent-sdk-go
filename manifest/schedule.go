package manifest

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron expression: minute hour day-of-month month
// day-of-week (0 or 7 = Sunday). Fields accept *, numbers, ranges (a-b),
// lists (a,b) and steps (*/n, a-b/n). When both day fields are
// restricted, a time matches if either does (classic cron).
type Schedule struct {
	expr                         string
	minute, hour, dom, month, dw uint64 // bit sets
	domStar, dowStar             bool
}

// ParseSchedule parses a cron expression.
func ParseSchedule(expr string) (Schedule, error) {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return Schedule{}, fmt.Errorf("schedule %q: want 5 fields (minute hour day month weekday)", expr)
	}
	s := Schedule{expr: expr}
	var err error
	if s.minute, err = cronField(f[0], 0, 59); err != nil {
		return Schedule{}, fmt.Errorf("schedule %q: minute: %w", expr, err)
	}
	if s.hour, err = cronField(f[1], 0, 23); err != nil {
		return Schedule{}, fmt.Errorf("schedule %q: hour: %w", expr, err)
	}
	if s.dom, err = cronField(f[2], 1, 31); err != nil {
		return Schedule{}, fmt.Errorf("schedule %q: day: %w", expr, err)
	}
	if s.month, err = cronField(f[3], 1, 12); err != nil {
		return Schedule{}, fmt.Errorf("schedule %q: month: %w", expr, err)
	}
	if s.dw, err = cronField(f[4], 0, 7); err != nil {
		return Schedule{}, fmt.Errorf("schedule %q: weekday: %w", expr, err)
	}
	if s.dw&(1<<7) != 0 { // 7 is Sunday too
		s.dw |= 1
	}
	s.domStar, s.dowStar = f[2] == "*", f[4] == "*"
	return s, nil
}

func cronField(field string, lo, hi int) (uint64, error) {
	var set uint64
	for _, part := range strings.Split(field, ",") {
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n < 1 || n > hi {
				return 0, fmt.Errorf("bad step %q", stepStr)
			}
			step = n
		}
		a, b := lo, hi
		if rng != "*" {
			from, to, isRange := strings.Cut(rng, "-")
			var err error
			if a, err = strconv.Atoi(from); err != nil {
				return 0, fmt.Errorf("bad value %q", from)
			}
			b = a
			if isRange {
				if b, err = strconv.Atoi(to); err != nil {
					return 0, fmt.Errorf("bad value %q", to)
				}
			} else if hasStep {
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return 0, fmt.Errorf("%q out of range %d-%d", part, lo, hi)
		}
		for v := a; v <= b; v += step {
			set |= 1 << v
		}
	}
	return set, nil
}

// Matches reports whether t (to the minute) is a scheduled time.
func (s Schedule) Matches(t time.Time) bool {
	if s.minute&(1<<t.Minute()) == 0 || s.hour&(1<<t.Hour()) == 0 || s.month&(1<<int(t.Month())) == 0 {
		return false
	}
	dom := s.dom&(1<<t.Day()) != 0
	dow := s.dw&(1<<int(t.Weekday())) != 0
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dow
	case s.dowStar:
		return dom
	default:
		return dom || dow
	}
}

// Next returns the first scheduled minute strictly after t (zero if none
// within a year).
func (s Schedule) Next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	for limit := t.AddDate(1, 0, 0); t.Before(limit); t = t.Add(time.Minute) {
		if s.Matches(t) {
			return t
		}
	}
	return time.Time{}
}

func (s Schedule) String() string { return s.expr }
