package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Discord snowflake epoch: 2015-01-01T00:00:00Z in milliseconds.
const discordEpoch int64 = 1420070400000

// timeToSnowflake returns the smallest snowflake ID created at or after t.
// Filtering messages by comparing their (numeric) IDs against this bound is
// exact, because a snowflake's high bits ARE its creation timestamp.
func timeToSnowflake(t time.Time) uint64 {
	ms := t.UnixMilli() - discordEpoch
	if ms < 0 {
		return 0
	}
	// The timestamp occupies the top 42 bits; a larger ms (dates past ~2154)
	// would overflow the shift and wrap to a small value, silently filtering
	// out everything. Saturate to the max snowflake instead, so a far-future
	// upper bound correctly means "no bound".
	const maxMs = int64(1)<<42 - 1
	if ms > maxMs {
		return ^uint64(0)
	}
	return uint64(ms) << 22
}

// snowflakeToTime recovers the creation time encoded in a snowflake ID.
func snowflakeToTime(id uint64) time.Time {
	ms := int64(id>>22) + discordEpoch
	return time.UnixMilli(ms)
}

var dateLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02T15:04",
	"2006-01-02",
	"2006/01/02",
	"01/02/2006",
}

// parseDate accepts several common date/datetime formats. Bare dates are
// interpreted in the machine's local time zone.
func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range dateLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("could not parse date %q (try YYYY-MM-DD or RFC3339)", s)
}

// parseWindow turns a relative window into a cutoff time measured back from
// `now`. Accepts keywords (hour, day, week, month, year, singular or plural),
// "today" (since local midnight, not the last 24 hours), and shorthand:
// Nh (hours), Nd (days), Nw (weeks), Nmo (months), Ny (years). Months and
// years use calendar arithmetic, not fixed durations.
func parseWindow(s string, now time.Time) (time.Time, error) {
	raw := strings.ToLower(strings.TrimSpace(s))
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty window")
	}

	switch strings.TrimSuffix(raw, "s") {
	case "hour":
		return now.Add(-time.Hour), nil
	case "today":
		// Since local midnight; "day" below is the rolling 24 hours.
		y, mo, d := now.Date()
		return time.Date(y, mo, d, 0, 0, 0, 0, now.Location()), nil
	case "day":
		return now.AddDate(0, 0, -1), nil
	case "week":
		return now.AddDate(0, 0, -7), nil
	case "month":
		return now.AddDate(0, -1, 0), nil
	case "year":
		return now.AddDate(-1, 0, 0), nil
	}

	i := 0
	for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
		i++
	}
	if i == 0 {
		return time.Time{}, fmt.Errorf("invalid window %q (use e.g. 7d, 2w, 3mo, 1y, or month)", s)
	}
	n, err := strconv.Atoi(raw[:i])
	if err != nil || n < 0 {
		return time.Time{}, fmt.Errorf("invalid window %q", s)
	}
	unit := raw[i:]
	switch unit {
	case "h", "hr", "hour", "hours":
		return now.Add(-time.Duration(n) * time.Hour), nil
	case "d", "day", "days":
		return now.AddDate(0, 0, -n), nil
	case "w", "wk", "week", "weeks":
		return now.AddDate(0, 0, -7*n), nil
	case "mo", "mon", "month", "months":
		return now.AddDate(0, -n, 0), nil
	case "y", "yr", "year", "years":
		return now.AddDate(-n, 0, 0), nil
	default:
		return time.Time{}, fmt.Errorf("unknown window unit %q in %q (use h, d, w, mo, y)", unit, s)
	}
}

// TimeBounds holds the resolved snowflake range for a run.
type TimeBounds struct {
	AfterID, BeforeID uint64
}

// resolveBounds merges every lower-bound source (explicit --after snowflake,
// --after-date, --last) into a single AfterID (the tightest), and every
// upper-bound source (--before snowflake, --before-date) into BeforeID.
func resolveBounds(afterSnow, beforeSnow, afterDate, beforeDate, last string, now time.Time) (TimeBounds, error) {
	var tb TimeBounds

	// Lower bounds (keep messages NEWER than these).
	if afterSnow != "" {
		id, err := parseSnowflake(afterSnow)
		if err != nil {
			return tb, err
		}
		tb.AfterID = max(tb.AfterID, id)
	}
	if afterDate != "" {
		t, err := parseDate(afterDate)
		if err != nil {
			return tb, err
		}
		if id := timeToSnowflake(t); id > tb.AfterID {
			tb.AfterID = id
		}
	}
	if last != "" {
		t, err := parseWindow(last, now)
		if err != nil {
			return tb, err
		}
		if id := timeToSnowflake(t); id > tb.AfterID {
			tb.AfterID = id
		}
	}

	// Upper bounds (keep messages OLDER than these).
	if beforeSnow != "" {
		id, err := parseSnowflake(beforeSnow)
		if err != nil {
			return tb, err
		}
		tb.BeforeID = minNonZeroU64(tb.BeforeID, id)
	}
	if beforeDate != "" {
		t, err := parseDate(beforeDate)
		if err != nil {
			return tb, err
		}
		id := timeToSnowflake(t)
		if tb.BeforeID == 0 || id < tb.BeforeID {
			tb.BeforeID = id
		}
	}

	if tb.AfterID != 0 && tb.BeforeID != 0 && tb.AfterID >= tb.BeforeID {
		return tb, fmt.Errorf("empty date range: 'after' bound is not before 'before' bound")
	}
	return tb, nil
}

func minNonZeroU64(a, b uint64) uint64 {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}
