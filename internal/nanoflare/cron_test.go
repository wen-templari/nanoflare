package nanoflare

import (
	"testing"
	"time"
)

func TestNormalizeTriggers(t *testing.T) {
	triggers, err := NormalizeTriggers(TriggerConfig{Crons: []string{" */5 * * * * ", "0 12 * * 1-5"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(triggers.Crons) != 2 || triggers.Crons[0] != "*/5 * * * *" || triggers.Crons[1] != "0 12 * * 1-5" {
		t.Fatalf("triggers = %#v", triggers)
	}
}

func TestNormalizeTriggersRejectsInvalidCrons(t *testing.T) {
	for _, test := range []TriggerConfig{
		{Crons: []string{""}},
		{Crons: []string{"* * * *"}},
		{Crons: []string{"60 * * * *"}},
		{Crons: []string{"0 0 LW * *"}},
		{Crons: []string{"*/0 * * * *"}},
		{Crons: []string{"0 0 * * *", "0 0 * * *"}},
	} {
		t.Run("", func(t *testing.T) {
			if _, err := NormalizeTriggers(test); err == nil {
				t.Fatalf("NormalizeTriggers(%#v) succeeded, want error", test)
			}
		})
	}
}

func TestCronScheduleMatchesUTC(t *testing.T) {
	schedule, err := ParseCron("*/15 8-10 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}
	if !schedule.Matches(time.Date(2026, 7, 10, 8, 30, 22, 0, time.UTC)) {
		t.Fatal("schedule should match Friday 08:30 UTC")
	}
	if schedule.Matches(time.Date(2026, 7, 11, 8, 30, 0, 0, time.UTC)) {
		t.Fatal("schedule should not match Saturday")
	}
}

func TestCronScheduleDayOfMonthOrWeek(t *testing.T) {
	schedule, err := ParseCron("0 8 1 * 5")
	if err != nil {
		t.Fatal(err)
	}
	if !schedule.Matches(time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)) {
		t.Fatal("schedule should match the first day of the month")
	}
	if !schedule.Matches(time.Date(2026, 7, 3, 8, 0, 0, 0, time.UTC)) {
		t.Fatal("schedule should match Friday")
	}
	if schedule.Matches(time.Date(2026, 7, 2, 8, 0, 0, 0, time.UTC)) {
		t.Fatal("schedule should not match Thursday the second")
	}
}
