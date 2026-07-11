package nanoflare

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type CronSchedule struct {
	expression string
	minutes    cronField
	hours      cronField
	days       cronField
	months     cronField
	weekdays   cronField
}

type cronField struct {
	values   map[int]bool
	wildcard bool
}

func NormalizeTriggers(config TriggerConfig) (TriggerConfig, error) {
	if len(config.Crons) == 0 {
		return TriggerConfig{}, nil
	}
	seen := make(map[string]bool, len(config.Crons))
	crons := make([]string, 0, len(config.Crons))
	for _, raw := range config.Crons {
		cron := strings.TrimSpace(raw)
		if cron == "" {
			return TriggerConfig{}, fmt.Errorf("cron trigger must not be empty")
		}
		if seen[cron] {
			return TriggerConfig{}, fmt.Errorf("duplicate cron trigger %q", cron)
		}
		if _, err := ParseCron(cron); err != nil {
			return TriggerConfig{}, err
		}
		seen[cron] = true
		crons = append(crons, cron)
	}
	return TriggerConfig{Crons: crons}, nil
}

func ParseCron(expression string) (CronSchedule, error) {
	expression = strings.TrimSpace(expression)
	parts := strings.Fields(expression)
	if len(parts) != 5 {
		return CronSchedule{}, fmt.Errorf("cron trigger %q must have five fields", expression)
	}
	fields := []struct {
		name     string
		min, max int
		value    *cronField
	}{
		{name: "minute", min: 0, max: 59},
		{name: "hour", min: 0, max: 23},
		{name: "day of month", min: 1, max: 31},
		{name: "month", min: 1, max: 12},
		{name: "day of week", min: 0, max: 7},
	}
	schedule := CronSchedule{expression: expression}
	fields[0].value = &schedule.minutes
	fields[1].value = &schedule.hours
	fields[2].value = &schedule.days
	fields[3].value = &schedule.months
	fields[4].value = &schedule.weekdays
	for i, part := range parts {
		field, err := parseCronField(part, fields[i].min, fields[i].max)
		if err != nil {
			return CronSchedule{}, fmt.Errorf("invalid %s in cron trigger %q: %w", fields[i].name, expression, err)
		}
		if i == 4 && field.values[7] {
			field.values[0] = true
			delete(field.values, 7)
		}
		*fields[i].value = field
	}
	return schedule, nil
}

func (s CronSchedule) Expression() string {
	return s.expression
}

func (s CronSchedule) Matches(t time.Time) bool {
	t = t.UTC()
	dayMatches := s.days.matches(t.Day())
	weekdayMatches := s.weekdays.matches(int(t.Weekday()))
	if !s.days.wildcard && !s.weekdays.wildcard {
		dayMatches = dayMatches || weekdayMatches
	} else {
		dayMatches = dayMatches && weekdayMatches
	}
	return s.minutes.matches(t.Minute()) &&
		s.hours.matches(t.Hour()) &&
		s.months.matches(int(t.Month())) &&
		dayMatches
}

func parseCronField(raw string, minValue, maxValue int) (cronField, error) {
	if raw == "" {
		return cronField{}, fmt.Errorf("field is empty")
	}
	field := cronField{values: map[int]bool{}, wildcard: raw == "*"}
	for _, item := range strings.Split(raw, ",") {
		if item == "" {
			return cronField{}, fmt.Errorf("list contains an empty item")
		}
		step := 1
		base := item
		if before, after, ok := strings.Cut(item, "/"); ok {
			base = before
			parsed, err := strconv.Atoi(after)
			if err != nil || parsed <= 0 {
				return cronField{}, fmt.Errorf("step %q must be a positive number", after)
			}
			step = parsed
		}
		start, end, err := parseCronRange(base, minValue, maxValue)
		if err != nil {
			return cronField{}, err
		}
		for value := start; value <= end; value += step {
			field.values[value] = true
		}
	}
	if len(field.values) == 0 {
		return cronField{}, fmt.Errorf("field matches no values")
	}
	return field, nil
}

func parseCronRange(raw string, minValue, maxValue int) (int, int, error) {
	if raw == "*" {
		return minValue, maxValue, nil
	}
	if strings.ContainsAny(raw, "LW#?") {
		return 0, 0, fmt.Errorf("advanced cron syntax is not supported in v1")
	}
	if left, right, ok := strings.Cut(raw, "-"); ok {
		start, err := parseCronNumber(left, minValue, maxValue)
		if err != nil {
			return 0, 0, err
		}
		end, err := parseCronNumber(right, minValue, maxValue)
		if err != nil {
			return 0, 0, err
		}
		if start > end {
			return 0, 0, fmt.Errorf("range start %d is after end %d", start, end)
		}
		return start, end, nil
	}
	value, err := parseCronNumber(raw, minValue, maxValue)
	if err != nil {
		return 0, 0, err
	}
	return value, value, nil
}

func parseCronNumber(raw string, minValue, maxValue int) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", raw)
	}
	if value < minValue || value > maxValue {
		return 0, fmt.Errorf("%d is outside %d-%d", value, minValue, maxValue)
	}
	return value, nil
}

func (f cronField) matches(value int) bool {
	return f.values[value]
}
