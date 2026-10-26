package guild

import (
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

// RankingMemberFacts contains observations from the authoritative account and
// character repositories. Newbie is an explicit status, never inferred from
// merely joining a guild after the catalog's historical promotion date.
// Owned soft-deleted Character records prove exclusion without removing guild
// membership or accepting an unverified missing fact.
type RankingMemberFacts struct {
	Owner          r.GuildOwner
	AdventureLevel int
	PVPGrade       int
	Newbie         bool
	Online         bool
	Character      r.CharacterRecord
}

// RankingMemberFactors computes the member/account categories. Weekly UV uses
// the monthly average of distinct weekly observations. Gameplay and purification
// factors are supplied by their own committed producers.
func RankingMemberFactors(g *Guild, facts map[string]RankingMemberFacts, rules *RankingRules, now time.Time) (RankingFactors, error) {
	var out RankingFactors
	if g == nil || rules == nil || now.IsZero() {
		return out, ErrRankingRules
	}
	var active float64
	var adventure, pvp, raid float64
	var members int
	for id, m := range g.Members {
		f, ok := facts[id]
		if !ok || f.Owner.CharacterID != id || f.Owner.AccountID != m.AccountID || f.Character.CharacterID != id || f.Character.AccountID != m.AccountID {
			return RankingFactors{}, ErrCharacterNotFound
		}
		if f.Character.Stats["delete_flag"] != 0 {
			continue
		}
		if f.AdventureLevel < 0 || f.PVPGrade < 0 {
			return RankingFactors{}, ErrCharacterNotFound
		}
		members++
		observations, err := rankingWeeklyPresence(m, f.Online, now)
		if err != nil {
			return RankingFactors{}, err
		}
		active += observations
		adventure += RankingLevelPoints(rules.Adventure, f.AdventureLevel, f.Newbie)
		pvp += RankingLevelPoints(rules.PVP, f.PVPGrade, f.Newbie)
		start := RankingMonthStart(now)
		if m.JoinedAt.After(start) {
			start = m.JoinedAt
		}
		for mode, name := range map[byte]string{1: "anton", 2: "luke", 3: "fiendwar"} {
			points, ok := rules.RaidClears[name]
			if !ok {
				return RankingFactors{}, ErrRankingRules
			}
			value := points.Points
			if f.Newbie {
				value = points.NewbiePoints
			}
			raid += value * float64(recordedRankingRaidClears(f.Character, mode, start, now))
		}
	}
	out.Members = float32(math.Min(rules.Members.Maximum, float64(members)*rules.Members.Rate))
	out.WeeklyActive = float32(math.Min(rules.WeeklyActive.Maximum, active*rules.WeeklyActive.Rate))
	out.Adventure, out.PVP, out.RaidClear = float32(adventure), float32(pvp), float32(raid)
	if _, err := out.Total(); err != nil {
		return RankingFactors{}, err
	}
	return out, nil
}

// Use permanent terminal-clear receipts. Weekly counters and the last fifty
// mileage rows cannot recover the actual month or instance of a raid clear.
func recordedRankingRaidClears(character r.CharacterRecord, mode byte, since, until time.Time) int {
	prefix := fmt.Sprintf("raid_clear_v1_%d_", mode)
	var total int
	for key, stamp := range character.Stats {
		if stamp < since.Unix() || stamp > until.Unix() || !strings.HasPrefix(key, prefix) {
			continue
		}
		id, err := hex.DecodeString(strings.TrimPrefix(key, prefix))
		if err == nil && len(id) == 16 {
			total++
		}
	}
	return total
}
