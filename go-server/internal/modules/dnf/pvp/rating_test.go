package pvp

import (
	"context"
	"math"
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestPVPRatingSettlementRulesAndDurability(t *testing.T) {
	c := testCatalog(t)
	for _, tc := range []struct {
		name                                           string
		exp, grade, rp, played, wins, losses, opponent int64
		won, draw, manual, rated                       bool
		wantEXP, wantGrade, wantRP                     int64
	}{
		{name: "seed win", won: true, wantEXP: 1000, wantRP: 1050},
		{name: "seed loss", wantRP: 950},
		{name: "grade up", exp: 11950, rp: 1000, played: 11, opponent: 1000, won: true, wantEXP: 12950, wantGrade: 1, wantRP: 1011},
		{name: "grade down signed loss", exp: 12010, grade: 1, rp: 1000, played: 11, opponent: 1000, wantEXP: 11796, wantRP: 989},
		{name: "warmup completion", rp: 1000, played: 9, opponent: 1000, won: true, wantEXP: 1000, wantRP: 1050},
		{name: "first post warmup bonus", rp: 1000, played: 10, opponent: 1000, won: true, wantEXP: 1000, wantRP: 1051},
		{name: "four win multiplier", rp: 1000, played: 11, wins: 3, opponent: 1000, won: true, wantEXP: 1000, wantRP: 1013},
		{name: "two loss multiplier", rp: 1000, played: 11, losses: 1, opponent: 1000, wantRP: 987},
		{name: "higher rating draw", rp: 1500, played: 11, opponent: 1000, draw: true, wantRP: 1494},
		{name: "manual win", rp: 1234, manual: true, won: true, wantEXP: 450, wantRP: 1234},
		{name: "manual loss", exp: 12500, grade: 1, rp: 1234, manual: true, wantEXP: 12307, wantGrade: 1, wantRP: 1234},
		{name: "zero floor stays initialized", rp: 0, played: 11, opponent: 1000, rated: true, wantRP: 0},
		{name: "exp cap", exp: 26990, grade: 1, rp: 1000, played: 11, opponent: 1000, won: true, wantEXP: 27000, wantGrade: 1, wantRP: 1011},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			now := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
			stats := map[string]int64{ExperienceKey: tc.exp, GradeKey: tc.grade, RankPointKey: tc.rp, MaxRankKey: tc.rp,
				CertificationKey: tc.played, "pvp_win_streak": tc.wins, "pvp_loss_streak": tc.losses, "exp": 76543}
			if tc.rated {
				stats["pvp_rating_initialized"] = 1
			}
			if err := repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "1", AccountID: "a", Stats: stats}); err != nil {
				t.Fatal(err)
			}
			before, err := Prepare(ctx, repos, c, "a", "1", now)
			if err != nil {
				t.Fatal(err)
			}
			match := Match{Key: tc.name, StartedAt: now, CompletedAt: now.Add(time.Minute), Won: tc.won, Draw: tc.draw,
				Manual: tc.manual, OpponentRP: tc.opponent}
			failed := repos
			failed.CharacterSettlement = failCommit{repos.CharacterSettlement}
			if _, err := Complete(ctx, failed, c, "a", "1", match); err == nil {
				t.Fatal("commit failure ignored")
			}
			unchanged, _, _ := repos.Character.Load(ctx, "1")
			if !reflect.DeepEqual(unchanged.Stats, before.Character.Stats) {
				t.Fatal("failed transaction changed rating/experience")
			}
			for retry := 0; retry < 2; retry++ {
				after, err := Complete(ctx, repos, c, "a", "1", match)
				if err != nil {
					t.Fatal(err)
				}
				s := after.Character.Stats
				if s[ExperienceKey] != tc.wantEXP || s[GradeKey] != tc.wantGrade || s[RankPointKey] != tc.wantRP || after.ExperienceDelta != tc.wantEXP-tc.exp || after.BeforeGrade == nil || *after.BeforeGrade != uint32(tc.grade) {
					t.Fatalf("retry=%d exp=%d grade=%d rp=%d delta=%d", retry, s[ExperienceKey], s[GradeKey], s[RankPointKey], after.ExperienceDelta)
				}
				if s["exp"] != 76543 || s[MaxRankKey] != max(tc.rp, tc.wantRP) {
					t.Fatal("normal exp or highest RP corrupted")
				}
				wantPlayed := tc.played + 1
				if tc.manual {
					wantPlayed = tc.played
				}
				if s[CertificationKey] != wantPlayed {
					t.Fatal("mode or receipt replay altered certification")
				}
				if tc.manual && (!reflect.DeepEqual(after.Rows, before.Rows) || s[WinsKey] != 0 || s[LossesKey] != 0 || s["pvp_rating_initialized"] != before.Character.Stats["pvp_rating_initialized"]) {
					t.Fatal("manual result mutated ranked facts")
				}
			}
			restored, err := Prepare(ctx, repos, c, "a", "1", now.Add(24*time.Hour))
			if err != nil || restored.Character.Stats[RankPointKey] != tc.wantRP || restored.Character.Stats[ExperienceKey] != tc.wantEXP {
				t.Fatal("day reset/reconnect lost rating")
			}
		})
	}
	for _, key := range []string{ExperienceKey, GradeKey, RankPointKey, MaxRankKey, "pvp_win_streak"} {
		stats := map[string]int64{key: math.MaxInt32 + 1}
		if _, err := c.applyRating(stats, Match{}, 0); err == nil {
			t.Fatalf("invalid %s accepted", key)
		}
	}
}

func TestPVFRatingRulesRejectMissingAndMalformedTables(t *testing.T) {
	for _, replacement := range []string{
		"[pvp grade point]\n0 1000\n[/pvp grade point]",
		"[pvp experience]\n1 12000 2 27000\n[/pvp experience]",
	} {
		s := dailyTestSource()
		s["etc/pvp_ref.etc"] = replacement
		if _, err := Load(s); err == nil {
			t.Fatal("incomplete grade/mode data accepted")
		}
	}
	s := dailyTestSource()
	delete(s, "etc/matchingmode.etc")
	if _, err := Load(s); err == nil {
		t.Fatal("missing rating configuration silently substituted")
	}
}
