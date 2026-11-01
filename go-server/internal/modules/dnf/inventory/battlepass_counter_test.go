package inventory

import (
	"context"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestBattlePassCardCompoundUpgradeAndFailure(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		t.Run(fmt.Sprint(upgrade), func(t *testing.T) {
			o, g := cardTestOwner(t, map[string]repo.ItemStack{"0:241": cardTestStack(100, 2, 0), "0:68": {ItemID: 900, Count: 1}})
			cmd := MonsterCardCommand{CharacterID: 77, TargetSlot: 241, MaterialSlot: 241, MaterialCount: 1, BinderSlot: 68, InputSlots: []int16{241, 241}, Random: func(int) int { return 0 }}
			kind := uint32(21)
			apply := func() (MonsterCardResult, error) {
				return o.CompoundMonsterCards(context.Background(), cmd, cardTestResolve, cardTestRules())
			}
			if upgrade {
				kind = 31
				apply = func() (MonsterCardResult, error) {
					return o.UpgradeMonsterCard(context.Background(), cmd, cardTestResolve, cardTestRules())
				}
			}
			out, err := apply()
			if err != nil || !out.Applied {
				t.Fatal(out, err)
			}
			key := fmt.Sprintf("%s%s:%d:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(time.Now()), kind)
			ch, _, _ := g.Character.Load(nil, "77")
			if ch.Stats[key] != 1 {
				t.Fatal("卡片成功操作没有进度", ch.Stats)
			}
			if out, err = apply(); err != nil || out.Applied {
				t.Fatal("材料不足未拒绝", out, err)
			}
			ch, _, _ = g.Character.Load(nil, "77")
			if ch.Stats[key] != 1 {
				t.Fatal("失败重复计数")
			}
		})
	}
}
