package pvp

import (
	"context"
	"errors"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestPrivateCreationPVFAndEligibility(t *testing.T) {
	source := textSource{
		"etc/pvpparameter.etc":    "[pvp revision level]\n85\n[check level create private charac]\n85\n[pvp matching possible character]\n0 3 1 2\n[/pvp matching possible character]\n",
		"character/character.lst": "0 `swordman.chr`\n1 `fighter.chr`\n",
		"character/swordman.chr":  "[growtype 2]\n[pvp private create equipment list]\n`[weapon]` 11 1\n[/pvp private create equipment list]\n[growtype 4]\n[pvp private create equipment list]\n`[weapon]` 22 1 `[coat]` 23 1 `[weapon]` 24 0\n[/pvp private create equipment list]\n[pvp private recreate penaly period]\n30\n",
		"character/fighter.chr":   "[growtype 3]\n[pvp private create equipment list]\n`[weapon]` 44 1\n[/pvp private create equipment list]\n[pvp private recreate penaly period]\n30\n",
	}
	plan, err := LoadPrivateCreation(source, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Level != 85 || plan.RecreateDays != 30 || len(plan.Equipment) != 3 || plan.Equipment[0].ItemID != 22 || plan.Equipment[2].Equipped {
		t.Fatalf("plan=%+v", plan)
	}
	other, err := LoadPrivateCreation(source, 1, 2)
	if err != nil || other.Equipment[0].ItemID != 44 {
		t.Fatalf("other job=%+v err=%v", other, err)
	}
	if _, err := LoadPrivateCreation(source, 0, 2); err == nil {
		t.Fatal("accepted disabled grow")
	}
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name             string
		level            int
		private, deleted bool
		age              time.Duration
		want             byte
		rp               int64
	}{
		{name: "empty account"},
		{name: "low ordinary RP", level: 84, rp: 1234},
		{name: "high ordinary duplicate", level: 85, want: 150},
		{name: "dedicated duplicate", level: 1, private: true, want: 150},
		{name: "deleted cooldown", level: 85, private: true, deleted: true, age: 30*24*time.Hour - time.Second, want: 151},
		{name: "cooldown boundary inherits RP", level: 85, private: true, deleted: true, age: 30 * 24 * time.Hour, rp: 1400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repos := memory.NewMemoryGroup()
			if tc.level > 0 {
				stats := map[string]int64{"grow_type": 19, RankPointKey: tc.rp}
				if tc.private {
					stats[PrivateCharacterKey] = 1
				}
				if tc.deleted {
					stats["delete_flag"] = 1
				}
				if err := repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "1", AccountID: "account", Job: "0", Level: tc.level, Stats: stats, UpdatedAt: now.Add(-tc.age)}); err != nil {
					t.Fatal(err)
				}
			}
			rp, err := plan.Check(ctx, repos.Character, "account", now)
			if tc.want == 0 {
				if err != nil || rp != tc.rp {
					t.Fatalf("rp=%d err=%v", rp, err)
				}
			} else if !errors.Is(err, PrivateCreateError(tc.want)) {
				t.Fatalf("err=%v want=%d", err, tc.want)
			}
			if _, err := plan.Check(ctx, repos.Character, "another-account", now); err != nil {
				t.Fatalf("cross-account restriction: %v", err)
			}
		})
	}
}
