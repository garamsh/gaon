package job

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// field is one of the five fields of a schedule: its bounds, and the regular
// expression for one number in it.
type field struct {
	min, max int
	number   string
}

// The fields of a schedule, in order: minute, hour, day of month, month and
// day of week. Day of week takes 7 for Sunday, as 0.
var fields = [5]field{
	{0, 59, `[0-5]?[0-9]`},
	{0, 23, `[01]?[0-9]|2[0-3]`},
	{1, 31, `0?[1-9]|[12][0-9]|3[01]`},
	{1, 12, `0?[1-9]|1[0-2]`},
	{0, 7, `[0-7]`},
}

// SchedulePattern is the regular expression a schedule matches: five fields
// separated by one space, each a comma list of `*`, a number or a range, with
// an optional `/step` after a `*` or a range. It is the pattern of the
// definition's schema, so the API server refuses a schedule that does not match
// it, and ParseSchedule accepts what it accepts.
var SchedulePattern = func() string {
	var parts [5]string
	for i, f := range fields {
		n := "(?:" + f.number + ")"
		item := `(?:\*|` + n + `-` + n + `)(?:/[1-9][0-9]*)?|` + n
		parts[i] = "(?:" + item + ")(?:,(?:" + item + "))*"
	}
	return "^" + strings.Join(parts[:], " ") + "$"
}()

var scheduleRE = regexp.MustCompile(SchedulePattern)

// Schedule is a parsed five-field cron schedule, read in UTC.
type Schedule struct {
	// set[i] holds the values field i matches.
	set [5]map[int]bool
	// domStar and dowStar report whether the day of month and the day of week
	// begin with `*`.
	domStar, dowStar bool
}

// ParseSchedule parses a schedule of the form SchedulePattern states. A range
// whose end is below its start wraps through the field's maximum, so 22-2 in
// the hour field is 22, 23, 0, 1 and 2.
func ParseSchedule(expr string) (Schedule, error) {
	if !scheduleRE.MatchString(expr) {
		return Schedule{}, fmt.Errorf("schedule %q is not five fields of minute, hour, day of month, month and day of week", expr)
	}
	texts := strings.Split(expr, " ")
	var s Schedule
	for i, text := range texts {
		s.set[i] = parseField(text, fields[i])
	}
	s.domStar = strings.HasPrefix(texts[2], "*")
	s.dowStar = strings.HasPrefix(texts[4], "*")
	if s.set[4][7] {
		s.set[4][0] = true
	}
	return s, nil
}

// parseField expands a field the pattern accepted into the values it matches.
func parseField(text string, f field) map[int]bool {
	set := map[int]bool{}
	for _, item := range strings.Split(text, ",") {
		base, stepText, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			step, _ = strconv.Atoi(stepText)
		}
		lo, hi := f.min, f.max
		if base != "*" {
			from, to, isRange := strings.Cut(base, "-")
			lo, _ = strconv.Atoi(from)
			hi = lo
			if isRange {
				hi, _ = strconv.Atoi(to)
			}
		}
		var values []int
		for v := lo; v <= hi; v++ {
			values = append(values, v)
		}
		if lo > hi {
			for v := lo; v <= f.max; v++ {
				values = append(values, v)
			}
			for v := f.min; v <= hi; v++ {
				values = append(values, v)
			}
		}
		for i := 0; i < len(values); i += step {
			set[values[i]] = true
		}
	}
	return set
}

// matchesDay reports whether the day of t matches. As in cron, a day of month
// and a day of week that are both restricted match when either does.
func (s Schedule) matchesDay(t time.Time) bool {
	dom, dow := s.set[2][t.Day()], s.set[4][int(t.Weekday())]
	if s.domStar || s.dowStar {
		return dom && dow
	}
	return dom || dow
}

// horizon bounds the search of Next, so that a schedule that never fires, such
// as the 31st of February, ends it. It exceeds the longest gap between two 29ths
// of February.
const horizon = 9 * 365 * 24 * time.Hour

// Next returns the first scheduled minute after t, in UTC, and false when none
// falls within the horizon.
func (s Schedule) Next(t time.Time) (time.Time, bool) {
	t = t.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := t.Add(horizon)
	for t.Before(limit) {
		switch {
		case !s.set[3][int(t.Month())]:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !s.matchesDay(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
		case !s.set[1][t.Hour()]:
			t = t.Truncate(time.Hour).Add(time.Hour)
		case !s.set[0][t.Minute()]:
			t = t.Add(time.Minute)
		default:
			return t, true
		}
	}
	return time.Time{}, false
}

// LatestDue returns the last scheduled minute in the interval (after, now], and
// false when none falls in it.
func (s Schedule) LatestDue(after, now time.Time) (time.Time, bool) {
	var due time.Time
	found := false
	for t, ok := s.Next(after); ok && !t.After(now); t, ok = s.Next(t) {
		due, found = t, true
	}
	return due, found
}
