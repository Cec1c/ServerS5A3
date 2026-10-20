package inventory

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguardianshield "longheng.io/server/internal/modules/dnf/guardianshield"
)

func TestGuardianShieldPaladinDragonRejectMoveAndDeckSaveWithoutWrites(t *testing.T) {
	for _, grow := range []int64{3, 4, 0x13, 0x14, 0x23, 0x24} {
		for _, opcode := range []uint16{uint16(dnfenum.CmdPacketMoveItemspace), uint16(dnfenum.CmdPacketChangeDeckInfo)} {
			t.Run(fmt.Sprintf("grow_%02x_op%d", grow, opcode), func(t *testing.T) {
				ctx := context.Background()
				repos := newGuardianShieldHandlerRepositories(t)
				// 模拟历史改职遗留盾栏，拒绝请求不能覆盖旧记录或生成背包实物。
				seedGuardianShieldHandlerDeck(t, repos, dnfguardianshield.Deck{113370003, 113370004})
				character, _, _ := repos.Character.Load(ctx, "19")
				character.Stats["grow_type"] = grow
				if err := repos.Character.Save(ctx, character); err != nil {
					t.Fatal(err)
				}
				beforeCharacter, _, _ := repos.Character.Load(ctx, "19")
				beforeEquipment, _, _ := repos.Equipment.Load(ctx, "19")
				beforeInventory, _, _ := repos.Inventory.Load(ctx, "19")
				body := currentMoveItemspaceBody(34, 0, 113370005, 0, 33, 0, 0, 0, 19)
				wantFailure := []byte{0, moveItemspaceRejectedErrorCode, 34, 33}
				if opcode == uint16(dnfenum.CmdPacketChangeDeckInfo) {
					body = buildCurrentGuardianShieldDeckBody(dnfguardianshield.Deck{113370003, 113370005})
					wantFailure = []byte{0}
				}
				resolved := false
				result, err := NewHandler().Handle(ctx, alignedcmd.Request{
					Opcode: opcode, Body: body, AccountID: "dnf:1", SelectedCharacterID: 19, Repositories: repos,
					GuardianShield: func(ctx context.Context, itemID uint32, grow byte) (alignedcmd.GuardianShieldResolution, error) {
						resolved = true
						return guardianShieldHandlerResolver(ctx, itemID, grow)
					},
				})
				if err != nil || !result.Handled || !result.ResponseAllowed || len(result.UpperResponses) != 1 ||
					result.UpperResponses[0].Classification != 1 || result.UpperResponses[0].MsgID != opcode ||
					!bytes.Equal(result.UpperResponses[0].Body, wantFailure) || len(result.PostActions) != 0 || resolved {
					t.Fatalf("帕拉丁/龙骑士换盾未封闭拒绝：result=%+v err=%v resolved=%t", result, err, resolved)
				}
				afterCharacter, _, _ := repos.Character.Load(ctx, "19")
				afterEquipment, _, _ := repos.Equipment.Load(ctx, "19")
				afterInventory, _, _ := repos.Inventory.Load(ctx, "19")
				if !reflect.DeepEqual(beforeCharacter, afterCharacter) || !reflect.DeepEqual(beforeEquipment, afterEquipment) ||
					!reflect.DeepEqual(beforeInventory, afterInventory) {
					t.Fatal("拒绝后盾栏、装备投影或背包发生变化")
				}
			})
		}
	}
}
