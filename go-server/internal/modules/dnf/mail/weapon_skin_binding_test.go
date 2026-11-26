package mail

import (
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/weaponskin"
)

func TestWeaponSkinProductMailBinding(t *testing.T) {
	for _, tc := range []struct {
		name    string
		item    uint32
		account string
		allowed bool
	}{
		{"神奇成品不能寄给同账号角色", 10309604, "a", false},
		{"旧神奇成品也不能寄送", weaponskin.BeiyunProductID, "b", false},
		{"秘传成品不能跨账号", 490013604, "b", false},
		{"旧秘传成品也不能跨账号", weaponskin.AccountProductID, "b", false},
		{"秘传成品可在同账号流转", 490013604, "a", true},
		{"普通物品沿用原邮件逻辑", 1001, "b", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{
				CharacterID: "77", AccountID: "a", Name: "sender", Stats: map[string]int64{"gold": 5000},
			})
			mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{
				CharacterID: "88", AccountID: tc.account, Name: "receiver", Slot: 1,
			})
			err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
				CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{"0:65": {ItemID: int64(tc.item), Count: 1}},
			})
			if err != nil {
				t.Fatal(err)
			}
			owner := NewOwner(repos, func(uint32) (alignedcmd.MailboxItemResolution, error) {
				return alignedcmd.MailboxItemResolution{Price: 100, SlotStart: 65, SlotEnd: 120}, nil
			})
			result, err := owner.Send(ctx, "77", SendRequest{RecipientName: "receiver",
				Attachments: []SendAttachment{{ListType: 0, SlotIndex: 65, ItemID: tc.item, Count: 1}},
			})
			if tc.allowed {
				if err != nil || result.MailID == "" {
					t.Fatalf("允许的邮件未提交：%+v, %v", result, err)
				}
				return
			}
			if !errors.Is(err, ErrMailboxAssetMismatch) {
				t.Fatalf("受限武器外观邮件应被拒绝：%v", err)
			}
			bag, _, _ := repos.Inventory.Load(ctx, "77")
			sender, _, _ := repos.Character.Load(ctx, "77")
			_, hasMail, loadErr := repos.Mailbox.Load(ctx, "88")
			if bag.Slots["0:65"].Count != 1 || sender.Stats["gold"] != 5000 || hasMail || loadErr != nil {
				t.Fatalf("拒绝后资产变化：背包=%+v 金币=%d 邮件=%t 错误=%v", bag.Slots, sender.Stats["gold"], hasMail, loadErr)
			}
		})
	}
}
