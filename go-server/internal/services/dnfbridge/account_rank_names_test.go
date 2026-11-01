package dnfbridge

import (
	"context"
	"encoding/binary"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestAccountRankAdventureNamesExtendedAndLegacyCompatible(t *testing.T) {
	s, id, now := playerStatsFixture(t)
	g, _ := s.repositoryGroup()
	ctx := context.Background()
	for account, name := range map[string]string{"a": "冒险团甲", "b": "冒险团乙"} {
		if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: account, RepresentAccountName: name}); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []byte{0, 1} {
		raw := rankRequest(1, 0, 2, mode, 0)
		copy(raw, "RKQ2")
		r, ok := decodeAccountRankRequest(raw)
		if !ok || !r.named {
			t.Fatal("新版请求未接纳")
		}
		b, err := s.buildAccountRankGame(ctx, id, r, now)
		if err != nil {
			t.Fatal(err)
		}
		if string(b[:4]) != "RKA2" || len(b) != 128+144*int(b[7]) || rankName(b[64:128]) != "冒险团甲" {
			t.Fatal(b)
		}
		if rankName(b[208:272]) != "冒险团甲" {
			t.Fatal("首行冒险团错配", b)
		}
		if mode == 0 && rankName(b[352:416]) != "冒险团乙" {
			t.Fatal("其他账号冒险团错配", b)
		}
		if binary.LittleEndian.Uint64(b[20:28]) != 1 {
			t.Fatal("名次分数变化", b)
		}
	}
	r, _ := decodeAccountRankRequest(rankRequest(2, 0, 2, 0, 0))
	b, err := s.buildAccountRankGame(ctx, id, r, now)
	if err != nil || len(b) != 224 || string(b[:4]) != "RKA1" {
		t.Fatal("旧布局不兼容", b, err)
	}
	r.named = true
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	b, err = s.buildAccountRankGame(ctx, id, r, now)
	if err != nil || rankName(b[64:128]) != "未命名冒险团" {
		t.Fatal(b, err)
	}
}

func TestAccountRankColorlessAndGoldUseActualCounters(t *testing.T) {
	s, id, now := playerStatsFixture(t)
	g, _ := s.repositoryGroup()
	ctx := context.Background()
	for _, row := range []struct {
		metric, source string
		item           uint32
		amount         int64
	}{
		{repo.ActivityColorless, "skill", 3037, 7},
		{repo.ActivityGoldRepair, "repair", 0, 13},
		{repo.ActivityGoldNPC, "npc_purchase", 0, 9999},
	} {
		err := g.CharacterSettlement.WithinCharacterSettlement(ctx, "2", func(tx repo.Group) error {
			_, err := tx.Activity.Record(ctx, repo.ActivityEvent{ID: row.metric, AccountID: "a", CharacterID: "2", Metric: row.metric, Amount: row.amount, ItemID: row.item, Source: row.source, OccurredAt: now})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for metric, want := range map[byte]uint64{3: 7, 4: 13} {
		for period := byte(0); period < 3; period++ {
			raw := rankRequest(1, metric, period, 0, 0)
			copy(raw, "RKQ2")
			r, ok := decodeAccountRankRequest(raw)
			if !ok {
				t.Fatal(raw)
			}
			b, err := s.buildAccountRankGame(ctx, id, r, now)
			if err != nil || len(b) != 272 || b[4] != metric || binary.LittleEndian.Uint64(b[20:28]) != want || binary.LittleEndian.Uint64(b[136:144]) != want {
				t.Fatal(b, err)
			}
		}
	}
}
