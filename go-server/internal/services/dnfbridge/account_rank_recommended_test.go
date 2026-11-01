package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestAccountRankRecommendedReusesAdventureTotals(t *testing.T) {
	s, id, now := playerStatsFixture(t)
	g, _ := s.repositoryGroup()
	for charID, count := range map[string]int64{"2": 7, "10": 5, "7": 999, "99": 12} {
		ch, _, err := g.Character.Load(context.Background(), charID)
		if err != nil {
			t.Fatal(err)
		}
		if ch.Stats == nil {
			ch.Stats = map[string]int64{}
		}
		ch.Stats[repo.ActivityRecommendedDungeon] = count
		if err := g.Character.Save(context.Background(), ch); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.CharacterSettlement.WithinCharacterSettlement(context.Background(), "2", func(tx repo.Group) error {
		_, err := tx.Activity.Record(context.Background(), repo.ActivityEvent{ID: "rank-recommended", AccountID: "a", CharacterID: "2", Metric: repo.ActivityRecommendedDungeon, Amount: 1, OccurredAt: now, DungeonID: 310, Source: "recommended_clear"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for period := byte(0); period < 3; period++ {
		r, ok := decodeAccountRankRequest(rankRequest(1, 2, period, 0, 0))
		if !ok {
			t.Fatal("第三类指标未接纳")
		}
		b, err := s.buildAccountRankGame(context.Background(), id, r, now)
		if err != nil || b[4] != 2 {
			t.Fatal(b, err)
		}
		wantScore, wantAccounts := uint64(1), uint32(1)
		if period == 2 {
			wantScore, wantAccounts = 12, 2
		}
		if binary.LittleEndian.Uint64(b[20:28]) != wantScore || binary.LittleEndian.Uint32(b[8:12]) != wantAccounts {
			t.Fatal(period, b)
		}
	}
	r, _ := decodeAccountRankRequest(rankRequest(2, 2, 2, 1, 0))
	b, err := s.buildAccountRankGame(context.Background(), id, r, now)
	if err != nil || len(b) != 224 || rankName(b[80:144]) != "当前角色" || binary.LittleEndian.Uint64(b[72:80]) != 7 || binary.LittleEndian.Uint64(b[152:160]) != 5 {
		t.Fatal(b, err)
	}
}
