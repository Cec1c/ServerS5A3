package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Policy and protection identity are read from the actual PVF. The failure
// outcome and gold quote are fixtures; this is not a gameplay capture.
func TestRealPVFUpgradeFailureHandlerKeepsEquipmentAndTicketRules(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{initialEquipmentArchive: archive, pvfItemCatalog: catalog}
	policy := service.alignedUpgradePolicyResolver()
	protect, err := service.alignedUpgradeProtectTicketResolverForCommand(dnfenum.CmdPacketUpgradeItem)
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []byte{8, 9, 10} {
		t.Run(strconv.Itoa(int(level)), func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			target := realForgingWeaponStack(28309, 45)
			target.RawEntry[0x0a] = level
			target.Extra["amplify_type"] = "3"
			target.Extra["amplify_value"] = "5"
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 1000}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{
				"0:9": target, "0:121": {ItemID: 10093372, Count: 2}, "0:122": {ItemID: 3242, Count: 100},
			}}); err != nil {
				t.Fatal(err)
			}
			body := make([]byte, 16)
			binary.LittleEndian.PutUint16(body, 1)
			binary.LittleEndian.PutUint16(body[2:], 9)
			binary.LittleEndian.PutUint32(body[4:], 28309)
			binary.LittleEndian.PutUint16(body[8:], 122)
			binary.LittleEndian.PutUint16(body[10:], 65535)
			result, err := inventory.NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketUpgradeItem), Body: body, AccountID: "acc", SelectedCharacterID: 77, Repositories: repos,
				UpgradePolicyResolver: func(mode string, id int64, current int) (alignedcmd.UpgradePolicyResolution, error) {
					q, err := policy(mode, id, current)
					q.SuccessWeight = 0
					return q, err
				},
				UpgradeProtectTicketResolver: protect,
				UpgradeCostResolver: func(string, int64, int, bool, uint16) (alignedcmd.UpgradeCostResolution, error) {
					return alignedcmd.UpgradeCostResolution{Resolved: true, GoldCost: 100}, nil
				},
			})
			if err != nil || !result.Handled || !result.ResponseAllowed || len(result.UpperResponses) < 2 {
				t.Fatalf("handler=%+v err=%v", result, err)
			}
			ack := result.UpperResponses[0]
			if ack.MsgID != uint16(dnfenum.CmdPacketUpgradeItem) || len(ack.Body) < 15 || ack.Body[0] != 1 || ack.Body[1] != 1 || ack.Body[12] != 2 || ack.Body[13]&31 != 0 {
				t.Fatalf("failure ACK=%x", ack.Body)
			}
			bag, _, err := repos.Inventory.Load(ctx, "77")
			if err != nil {
				t.Fatal(err)
			}
			stored, found := bag.Slots["0:9"]
			wantTicket := int64(2)
			if level == 10 {
				wantTicket = 1
			}
			if !found || stored.RawEntry[0x0a]&31 != 0 || bag.Slots["0:122"].Count != 100-int64(level+1) || bag.Slots["0:121"].Count != wantTicket {
				t.Fatalf("level=%d target_present=%t material=%d ticket=%d", level, found, bag.Slots["0:122"].Count, bag.Slots["0:121"].Count)
			}
			char, _, _ := repos.Character.Load(ctx, "77")
			if char.Stats["gold"] != 900 {
				t.Fatal("failure gold was not committed exactly once")
			}
			for _, stack := range bag.Slots {
				if stack.ItemID == 3244 {
					t.Fatal("retained equipment incorrectly granted destruction compensation")
				}
			}
			if level < 10 && result.UpgradeProtectTicket != nil {
				t.Fatal("ordinary downgrade consumed protection")
			}
			if level == 10 && (result.UpgradeProtectTicket == nil || !result.UpgradeProtectTicket.Consumed) {
				t.Fatal("legitimate destruction did not use protection")
			}
			refresh := result.UpperResponses[1]
			if refresh.MsgID != 14 || refresh.Classification != 0 || len(refresh.Body) < 3 || refresh.Body[0] != 0 {
				t.Fatal("missing committed inventory refresh")
			}
			count := int(binary.LittleEndian.Uint16(refresh.Body[1:3]))
			width := len(stored.RawEntry)
			if width != 0x77 || len(refresh.Body) != 3+count*width {
				t.Fatalf("invalid inventory row shape: %x", refresh.Body)
			}
			seen := map[uint16]bool{}
			for i := 0; i < count; i++ {
				row := refresh.Body[3+i*width : 3+(i+1)*width]
				slot := binary.LittleEndian.Uint16(row[:2])
				seen[slot] = true
				if slot == 9 && (row[10]&31 != 0 || binary.LittleEndian.Uint32(row[2:6]) != 28309) {
					t.Fatal("target refresh disagreed with committed equipment")
				}
				if slot == 122 && binary.LittleEndian.Uint32(row[6:10]) != uint32(99-level) {
					t.Fatal("material refresh disagreed with committed count")
				}
				if slot == 121 && binary.LittleEndian.Uint32(row[6:10]) != uint32(wantTicket) {
					t.Fatal("protection refresh disagreed with committed count")
				}
			}
			if !seen[9] || !seen[122] || level == 10 && !seen[121] {
				t.Fatalf("committed slots missing from inventory refresh: %v", seen)
			}
		})
	}
}
