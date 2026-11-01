package inventory

import (
	"context"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

// 附魔夹具补齐真实角色归属，与新增的角色+宝珠+装备原子事务一致。
func saveEnchantTestInventory(t *testing.T, ctx context.Context, g repo.Group, record repo.InventoryRecord) {
	t.Helper()
	if _, found, err := g.Character.Load(ctx, record.CharacterID); err != nil {
		t.Fatal(err)
	} else if !found {
		if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: record.CharacterID, AccountID: "acc"}); err != nil {
			t.Fatal(err)
		}
	}
	saveTestInventory(t, ctx, g, record)
}

func TestBattlePassEnchantActualSuccessAndMissingBead(t *testing.T) {
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	saveEnchantFixture(t, ctx, g, 1, 0, 0)
	o, _ := NewOwner(g)
	if out, err := o.Enchant(ctx, newEnchantTestCommand(12, 30), coatEnchantResolver(9001, nil)); err != nil || !out.Success {
		t.Fatal(out, err)
	}
	if out, err := o.Enchant(ctx, newEnchantTestCommand(12, 30), coatEnchantResolver(9001, nil)); err != nil || out.Success {
		t.Fatal("无材料仍成功", out, err)
	}
	ch, _, _ := g.Character.Load(ctx, "77")
	key := fmt.Sprintf("%s%s:27:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(time.Now()))
	if ch.Stats[key] != 1 {
		t.Fatal(ch.Stats)
	}
}
