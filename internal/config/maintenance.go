package config

import (
	"fmt"
	"strings"
	"time"
)

// MaintenanceWindow suppresses alerts matching Matchers during a recurring
// daily time-of-day window, optionally restricted to certain weekdays. Unlike
// a one-shot Silence (which ends at a single RFC3339 instant), a maintenance
// window recurs every day (or every listed weekday) for backup/patch windows.
//
// Times are "HH:MM" in 24-hour clock, interpreted in the window's Timezone
// (an IANA name like "America/New_York"; empty means UTC). A window that wraps
// midnight (Start > End, e.g. 23:00-02:00) is supported and spans into the next
// day.
type MaintenanceWindow struct {
	// Name is a human label for logs/console (optional).
	Name string `yaml:"name"`
	// Matchers selects which alerts this window suppresses, with the same
	// semantics as silence matchers (namespace/reason accept anchored regexes).
	Matchers map[string]string `yaml:"matchers"`
	// Start and End are "HH:MM" local times. Start == End is an empty window
	// (suppresses nothing); use 00:00-00:00 to mean "never" and rely on Days,
	// or 00:00-23:59 for an all-day window.
	Start string `yaml:"start"`
	End   string `yaml:"end"`
	// Days optionally restricts the window to weekdays (lowercase 3-letter:
	// mon,tue,wed,thu,fri,sat,sun). Empty means every day.
	Days []string `yaml:"days"`
	// Timezone is an IANA location name; empty means UTC.
	Timezone string `yaml:"timezone"`
}

// minutesOfDay parses "HH:MM" into minutes since midnight [0,1440).
func minutesOfDay(hhmm string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &h, &m); err != nil {
		return 0, fmt.Errorf("time %q must be HH:MM", hhmm)
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("time %q out of range (00:00-23:59)", hhmm)
	}
	return h*60 + m, nil
}

var weekdayByAbbrev = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday,
	"wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// validate checks the window's fields are parseable. Called from Config.Validate
// so a bad window fails at load instead of silently never matching.
func (w MaintenanceWindow) validate() error {
	if len(w.Matchers) == 0 {
		return fmt.Errorf("matchers is empty (would suppress every alert)")
	}
	if _, err := minutesOfDay(w.Start); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	if _, err := minutesOfDay(w.End); err != nil {
		return fmt.Errorf("end: %w", err)
	}
	for _, d := range w.Days {
		if _, ok := weekdayByAbbrev[strings.ToLower(d)]; !ok {
			return fmt.Errorf("day %q must be one of sun,mon,tue,wed,thu,fri,sat", d)
		}
	}
	if w.Timezone != "" {
		if _, err := time.LoadLocation(w.Timezone); err != nil {
			return fmt.Errorf("timezone %q: %w", w.Timezone, err)
		}
	}
	return nil
}

// Active reports whether the window is in effect at instant t. The window's
// timezone is applied first; a window that wraps midnight is handled by checking
// the previous day's late portion as well. Day restrictions are evaluated
// against the local day the window's active minute belongs to.
func (w MaintenanceWindow) Active(t time.Time) bool {
	loc := time.UTC
	if w.Timezone != "" {
		if l, err := time.LoadLocation(w.Timezone); err == nil {
			loc = l
		}
	}
	lt := t.In(loc)
	start, err1 := minutesOfDay(w.Start)
	end, err2 := minutesOfDay(w.End)
	if err1 != nil || err2 != nil || start == end {
		return false // unparseable or empty window suppresses nothing
	}
	cur := lt.Hour()*60 + lt.Minute()

	if start < end {
		// Same-day window, e.g. 01:00-05:00.
		return cur >= start && cur < end && w.dayAllowed(lt.Weekday())
	}
	// Wrapping window, e.g. 23:00-02:00. Active either in the late part of the
	// start day (cur >= start) or the early part of the next day (cur < end).
	if cur >= start {
		return w.dayAllowed(lt.Weekday())
	}
	if cur < end {
		// We are in the morning portion that belongs to the PREVIOUS day's
		// window start, so test the previous weekday for the day restriction.
		return w.dayAllowed(prevWeekday(lt.Weekday()))
	}
	return false
}

func (w MaintenanceWindow) dayAllowed(d time.Weekday) bool {
	if len(w.Days) == 0 {
		return true
	}
	for _, name := range w.Days {
		if weekdayByAbbrev[strings.ToLower(name)] == d {
			return true
		}
	}
	return false
}

func prevWeekday(d time.Weekday) time.Weekday {
	return time.Weekday((int(d) + 6) % 7)
}
