package job_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/workloads/internal/job"
)

func utc(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParseSchedule_AcceptsExactlyWhatThePatternAccepts(t *testing.T) {
	pattern := regexp.MustCompile(job.SchedulePattern)
	tests := []struct {
		expr  string
		valid bool
	}{
		{"* * * * *", true},
		{"*/5 * * * *", true},
		{"0 3 * * 1-5", true},
		{"0,30 9-17 * * *", true},
		{"0 0 1 1 *", true},
		{"0 22-2 * * *", true},
		{"0 0 * * 7", true},
		{"00 00 * * *", true},
		{"", false},
		{"* * * *", false},
		{"* * * * * *", false},
		{" * * * * *", false},
		{"*  * * * *", false},
		{"60 * * * *", false},
		{"* 24 * * *", false},
		{"* * 0 * *", false},
		{"* * 32 * *", false},
		{"* * * 13 *", false},
		{"* * * * 8", false},
		{"*/0 * * * *", false},
		{"5/2 * * * *", false},
		{"*/ * * * *", false},
		{"1- * * * *", false},
		{"@daily", false},
		{"* * * jan *", false},
		{"* * * * mon", false},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			_, err := job.ParseSchedule(tt.expr)
			assert.Equal(t, tt.valid, err == nil, "ParseSchedule")
			assert.Equal(t, tt.valid, pattern.MatchString(tt.expr), "the schema pattern the API server applies")
		})
	}
}

func TestSchedule_NextIsTheFirstMatchingMinuteAfter(t *testing.T) {
	tests := []struct {
		name, expr, after, want string
	}{
		{"every fifteen minutes from the hour", "*/15 * * * *", "2026-11-13 12:00:00", "2026-11-13 12:15:00"},
		{"seconds are dropped", "*/15 * * * *", "2026-11-13 12:14:59", "2026-11-13 12:15:00"},
		{"the next day", "0 3 * * *", "2026-11-13 12:00:00", "2026-11-14 03:00:00"},
		{"a leap day two years on", "30 2 29 2 *", "2026-11-13 12:00:00", "2028-02-29 02:30:00"},
		{"day of month or day of week when both are set", "0 0 13 * 5", "2026-11-13 00:00:00", "2026-11-20 00:00:00"},
		{"day of month and a starred day of week", "0 0 13 * *", "2026-11-13 00:00:00", "2026-12-13 00:00:00"},
		{"seven is Sunday", "0 0 * * 7", "2026-11-13 00:00:00", "2026-11-15 00:00:00"},
		{"a range that wraps, into midnight", "0 22-1 * * *", "2026-11-13 23:00:00", "2026-11-14 00:00:00"},
		{"a range that wraps, out of it", "0 22-1 * * *", "2026-11-13 01:00:00", "2026-11-13 22:00:00"},
		{"a step inside a range", "10-30/10 * * * *", "2026-11-13 12:10:00", "2026-11-13 12:20:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := job.ParseSchedule(tt.expr)
			require.NoError(t, err)
			got, ok := s.Next(utc(tt.after))
			require.True(t, ok)
			assert.Equal(t, utc(tt.want), got)
		})
	}
}

func TestSchedule_NextEndsForAScheduleThatNeverFires(t *testing.T) {
	s, err := job.ParseSchedule("0 0 31 2 *")
	require.NoError(t, err)
	_, ok := s.Next(utc("2026-11-13 12:00:00"))
	assert.False(t, ok, "the 31st of February does not come")
}

func TestSchedule_LatestDueIsTheLastTimeInTheInterval(t *testing.T) {
	s, err := job.ParseSchedule("* * * * *")
	require.NoError(t, err)

	got, ok := s.LatestDue(utc("2026-11-13 12:00:00"), utc("2026-11-13 12:05:30"))
	require.True(t, ok)
	assert.Equal(t, utc("2026-11-13 12:05:00"), got, "five times passed; the latest is the one")

	// The control: the interval is open at its start and closed at its end.
	_, ok = s.LatestDue(utc("2026-11-13 12:05:00"), utc("2026-11-13 12:05:59"))
	assert.False(t, ok, "a time already dealt with is not due again")
	got, ok = s.LatestDue(utc("2026-11-13 12:04:00"), utc("2026-11-13 12:05:00"))
	require.True(t, ok)
	assert.Equal(t, utc("2026-11-13 12:05:00"), got, "a time that is now is due")
}
