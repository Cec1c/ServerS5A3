package itemusage

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestDailyAndWeeklyUsagePeriods(t *testing.T) {
	now := time.Date(2026, 10, 22, 6, 0, 0, 0, time.FixedZone("UTC+8", 8*3600)) // Thursday
	d := Definition{ItemID: 10162064, GroupID: -1, Daily: true, DailyLimit: 1, Weekly: true, WeeklyLimit: 2, WeeklyResetDay: time.Thursday}
	c := repo.CharacterRecord{CharacterID: "a", Stats: map[string]int64{"unrelated": 17}}
	if err := Consume(&c, d, now); err != nil {
		t.Fatal(err)
	}
	if a, b := Counts(c, d, now); a != 1 || b != 1 {
		t.Fatalf("counts=%d/%d", a, b)
	}
	for _, at := range []time.Time{now, now.Add(24*time.Hour - time.Nanosecond)} {
		before := repo.CloneCharacter(c)
		if err := Consume(&c, d, at); !errors.Is(err, ErrLimit) || !reflect.DeepEqual(c, before) {
			t.Fatalf("limit mutated counts: %v", err)
		}
	}
	if err := Consume(&c, d, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	before := repo.CloneCharacter(c)
	if err := Consume(&c, d, now.Add(48*time.Hour)); !errors.Is(err, ErrLimit) || !reflect.DeepEqual(c, before) {
		t.Fatalf("weekly limit partly reset daily state: %v", err)
	}
	if a, b := Counts(c, d, now.Add(7*24*time.Hour)); a != 0 || b != 0 {
		t.Fatalf("week reset=%d/%d", a, b)
	}
	if err := Consume(&c, d, now.Add(7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c.Stats["unrelated"] != 17 {
		t.Fatal("unrelated stats changed")
	}
	before = repo.CloneCharacter(c)
	if err := Consume(&c, d, now); !errors.Is(err, ErrClock) || !reflect.DeepEqual(c, before) {
		t.Fatalf("clock rewind changed counts: %v", err)
	}
}

func TestUsageIdentityAndExhaustion(t *testing.T) {
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	d := Definition{ItemID: 100, GroupID: -1, Daily: true, DailyLimit: 3}
	c := repo.CharacterRecord{CharacterID: "a"}
	for i := byte(1); i <= 3; i++ {
		if err := Consume(&c, d, now); err != nil {
			t.Fatal(err)
		}
		if got, _ := Counts(c, d, now); got != i {
			t.Fatalf("used=%d want=%d", got, i)
		}
	}
	before := repo.CloneCharacter(c)
	if err := Consume(&c, d, now); !errors.Is(err, ErrLimit) || !reflect.DeepEqual(c, before) {
		t.Fatal("fourth use changed state", err)
	}
	other := d
	other.ItemID++
	if got, _ := Counts(c, other, now); got != 0 {
		t.Fatal("different item shared count")
	}
	if got, _ := Counts(repo.CharacterRecord{CharacterID: "b"}, d, now); got != 0 {
		t.Fatal("different character shared count")
	}
	d.GroupID = 77
	other.GroupID = 77
	if err := Consume(&c, d, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := Counts(c, other, now); got != 1 {
		t.Fatal("explicit usable group not shared")
	}
}

func TestPVFUsageDefinition(t *testing.T) {
	for _, tc := range []struct {
		name, text       string
		invalid, limited bool
	}{
		{"none", "[cooltime group]\n77\n", false, false},
		{"daily", "[total usable count]\n3\n[cooltime group]\n77\n", false, true},
		{"weekly", "[total weekly usable count]\n2\n[total weekly usable count reset day]\n4\n", false, true},
		{"unlimited", "[total usable count]\n-1\n", false, false},
		{"zero", "[total usable count]\n0\n", false, true},
		{"overflow", "[total usable count]\n256\n", true, false},
		{"missing_value", "[total usable count]\n", true, false},
		{"missing_reset", "[total weekly usable count]\n2\n", true, false},
		{"bad_reset", "[total weekly usable count]\n2\n[total weekly usable count reset day]\n7\n", true, false},
		{"non_daily_event", "[usage limit count]\n3\n[use count limit all server item]\n100\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := pvf.Parse("stackable/test.stk", tc.text)
			if err != nil {
				t.Fatal(err)
			}
			d, err := FromPVF(100, doc)
			if (err != nil) != tc.invalid || (d != nil) != tc.limited {
				t.Fatalf("definition=%+v err=%v", d, err)
			}
			if d != nil && d.GroupID != -1 {
				t.Fatal("cooltime group became usage group")
			}
		})
	}
}
