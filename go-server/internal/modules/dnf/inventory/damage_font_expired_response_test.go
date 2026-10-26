package inventory

import (
	"bytes"
	"context"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	r "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"reflect"
	"testing"
	"time"
)

func TestDamageFontExpiredReturnsFailureWithoutConsuming(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	ch := r.CharacterRecord{CharacterID: "77", Stats: map[string]int64{DamageFontSelectedStatKey: 2, DamageFontOwnershipStatKey(2): 0}}
	bag := r.InventoryRecord{CharacterID: "77", Slots: map[string]r.ItemStack{slotKey(listTypeMain, 115): {ItemID: 10164635, Count: 2}}}
	saveTestCharacter(t, ctx, repos, ch)
	saveTestInventory(t, ctx, repos, bag)
	beforeCh, _, _ := repos.Character.Load(ctx, "77")
	beforeBag := loadTestInventory(t, ctx, repos, "77")
	body := []byte{115, 0, 0, 0, 0, 0, 0, 164, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	for i := 0; i < 2; i++ {
		result, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketUseStackableAction), Body: body, SelectedCharacterID: 77, Repositories: repos, DamageFontNow: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC), DamageFontResolver: func(int64) (alignedcmd.DamageFontResolution, error) {
			return alignedcmd.DamageFontResolution{Valid: true, ActionType: "[add damage font skin]", FontIndex: 6, ExpirationMode: alignedcmd.DamageFontExpirationFixed, FixedExpiration: time.Date(2018, 9, 29, 22, 0, 0, 0, time.UTC)}, nil
		}})
		if err != nil || !result.ResponseAllowed || len(result.UpperResponses) != 1 || !bytes.Equal(result.UpperResponses[0].Body, []byte{0, 1, 115, 0, 0, 164}) {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if len(result.ItemSlotRefreshes) != 0 || len(result.PostActions) != 1 || result.PostActions[0] != alignedcmd.PostActionDamageFontExpiredNotice {
			t.Fatalf("rejection projected success: %+v", result)
		}
	}
	afterCh, _, _ := repos.Character.Load(ctx, "77")
	afterBag := loadTestInventory(t, ctx, repos, "77")
	if !reflect.DeepEqual(beforeCh, afterCh) || !reflect.DeepEqual(beforeBag, afterBag) {
		t.Fatal("expired font changed ownership, selection, or inventory")
	}
}
