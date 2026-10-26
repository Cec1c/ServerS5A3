package inventory

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestPortableUsableItemLevelHandlerRound15(t *testing.T) {
	for _, tc := range []struct {
		name             string
		set              bool
		min, max, target int
		ok               bool
	}{
		{"minimum boundary", true, 1, 90, 1, true},
		{"maximum boundary", true, 1, 90, 90, true},
		{"below", true, 55, 90, 54, false},
		{"above", true, 1, 90, 91, false},
		{"unknown target", true, 1, 90, -1, false},
		{"zero target below min", true, 1, 90, 0, false},
		{"zero bounds are real restriction", true, 0, 0, 90, false},
		{"zero target fits zero bounds", true, 0, 0, 0, true},
		{"missing section keeps old path", false, 0, 0, -1, true},
		{"unbounded", true, -1, -1, -1, true},
		{"lower only", true, 55, -1, 54, false},
		{"upper only", true, -1, 90, 91, false},
		{"reversed", true, 90, 55, 75, false},
		{"malformed", true, -2, -2, 90, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(UpgradePortableConsumableEnv, UpgradePortableConsumableVendor)
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			c := repo.CharacterRecord{CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 12345}}
			before := repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{
				"0:9":   portableUpgradeTarget(portableUpgradeCase{mode: "amplify", targetLevel: 10}),
				"0:107": portableAmplifierStack(3),
			}}
			if err := g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			if err := g.Inventory.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			before, _, _ = g.Inventory.Load(ctx, "77")
			c, _, _ = g.Character.Load(ctx, "77")
			portable := portableAmplifyConsumable()
			portable.UsableItemLevelSet = tc.set
			portable.UsableItemLevelMin = tc.min
			portable.UsableItemLevelMax = tc.max
			portable.TargetMinimumLevel = tc.target
			body := make([]byte, 16)
			binary.LittleEndian.PutUint16(body, 1)
			binary.LittleEndian.PutUint16(body[2:], 9)
			binary.LittleEndian.PutUint32(body[4:], 700)
			binary.LittleEndian.PutUint16(body[8:], 107)
			binary.LittleEndian.PutUint16(body[10:], 65535)
			got, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketUpgradeItem), Body: body, AccountID: "acc", SelectedCharacterID: 77, Repositories: g, UpgradeTicketResolver: upgradeTicketResolverForPortable(portable), UpgradePolicyResolver: func(string, int64, int) (alignedcmd.UpgradePolicyResolution, error) {
				return alignedcmd.UpgradePolicyResolution{SuccessWeight: 100000, MaterialItemID: 3242, MaterialCount: 11}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if !got.Handled || len(got.UpperResponses) == 0 {
				t.Fatalf("no reply %+v", got)
			}
			ack := got.UpperResponses[0].Body
			after, _, _ := g.Inventory.Load(ctx, "77")
			afterC, _, _ := g.Character.Load(ctx, "77")
			if tc.ok {
				if len(ack) < 14 || ack[0] != 1 || upgradeLevelOf(after.Slots["0:9"]) != 11 || after.Slots["0:107"].Count != 2 {
					t.Errorf("valid portable settlement ack=%x inventory=%+v", ack, after)
				}
			} else {
				if !bytes.Equal(ack, []byte{0, upgradePortableRestrictionError}) {
					t.Errorf("restriction ack=%x", ack)
				}
				if !reflect.DeepEqual(before, after) {
					t.Error("rejected portable request changed inventory")
				}
				if len(got.UpperResponses) != 1 {
					t.Errorf("rejection projected updates=%+v", got.UpperResponses)
				}
			}
			if !reflect.DeepEqual(c, afterC) {
				t.Error("portable request changed character/gold")
			}
		})
	}
}
