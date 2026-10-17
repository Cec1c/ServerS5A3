package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"os"
	"reflect"
	"testing"

	dnfchronicle "longheng.io/server/internal/modules/dnf/chronicle"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestChronicleFailureNoticeCompatibilityOnlyChangesWornFailureDeleteSlot(t *testing.T) {
	r := dnfchronicle.Result{Request: dnfchronicle.Request{TargetList: 3, MaterialSlot: 6, TargetSlot: 20}, MaterialRemaining: 1,
		Rewards: []dnfchronicle.RewardSlot{{Slot: 122, ItemID: 3229, Count: 1}, {Slot: 124, ItemID: 3311, Count: 1}, {Slot: 358, ItemID: 3037, Count: 241}}}
	// 18:03:39现场失败包：只将无效的“主背包删除槽”改为非背包槽标记。
	captured, _ := hex.DecodeString("010600010000031400037a009d0c0000010000007c00ef0c0000010000006601dd0b0000f1000000")
	want := append([]byte(nil), captured...)
	want[7], want[8] = 0xff, 0xff
	if got := buildCurrentChronicleRefineSuccessBody(r); !bytes.Equal(got, want) {
		t.Fatalf("穿戴失败仅应改变删除槽；材料、失败标志和返材保持：%x", got)
	}
	r.Request.TargetList = 0
	captured[6] = 0
	if got := buildCurrentChronicleRefineSuccessBody(r); !bytes.Equal(got, captured) {
		t.Fatalf("背包装备碎装回包发生变化：%x", got)
	}
	for _, list := range []byte{0, 3} {
		r.Succeeded, r.Request.TargetList, r.Rewards = true, list, nil
		got := buildCurrentChronicleRefineSuccessBody(r)
		if len(got) != 10 || got[5] != 1 || got[6] != list || binary.LittleEndian.Uint16(got[7:9]) != 20 || got[9] != 0 {
			t.Fatalf("已验收的成功回包不应改变：%x", got)
		}
	}
}

func TestChronicleRealPVFWornFailureKeepsUnrelatedBackpackItemAndRewardPopup(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("未指定真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	s, session, conn, g := chronicleFixture(t, chronicleFixtureSource(), 1256)
	s.pvfItemCatalog = items
	saveChronicleWornFixture(t, g, 20, 100312515)
	ctx := context.Background()
	inv, _, _ := g.Inventory.Load(ctx, "2")
	inv.Slots["0:20"] = dnfrepo.ItemStack{ItemID: 100320756, Count: 1, Extra: map[string]string{"item_kind": "equipment", "quality_seed": "1234567"}}
	material := inv.Slots["0:65"]
	material.Count = 2
	inv.Slots["0:6"] = material
	delete(inv.Slots, "0:65")
	if err := g.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	untouched := inv.Slots["0:20"]
	eq, _, _ := g.Equipment.Load(ctx, "2")
	e := eq.Entries["20"]
	e.Extra["durability"] = "0"
	stack := dnfrepo.ItemStack{ItemID: e.ItemID, Count: 1, RawEntry: e.RawEntry, Extra: e.Extra}
	stack, err = currentSetChronicleOptions(stack, []dnfchronicle.Option{{AuraID: 1256, Job: 14, Grow: 4, EquipmentType: 20, Number: 7}})
	if err != nil {
		t.Fatal(err)
	}
	eq.Entries["20"] = currentWornChronicleUpdate(e, stack)
	if err := g.Equipment.Save(ctx, eq); err != nil {
		t.Fatal(err)
	}
	body, _ := hex.DecodeString("0600e8040000031400c3a5fa0505")
	request, err := parseCurrentChronicleRefineRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	m, err := items.ResolveItem(1256)
	if err != nil {
		t.Fatal(err)
	}
	cmd := s.currentChronicleRefineCommand(session, items, m, request)
	cmd.Random = func(int) int { return 77 } // 与18:03:39现场一致的普通第二次失败。
	owner, err := dnfchronicle.NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Refine(ctx, cmd)
	if err != nil || result.Succeeded || result.Probability != 11 || result.Roll != 77 || len(result.Rewards) != 3 {
		t.Fatalf("现场真实规则失败未复现：%+v %v", result, err)
	}
	if err := s.sendCurrentChronicleRefineResult(session, result); err != nil {
		t.Fatal(err)
	}
	data, ackSeen, wornCleared := conn.write.Bytes(), false, false
	for len(data) != 0 {
		packet, rest := splitGameServerUpperPacket(t, data)
		data = rest
		if packet.Header.MsgID == 370 {
			if !wornCleared || packet.Body[5] != 0 || packet.Body[9] != 3 || binary.LittleEndian.Uint16(packet.Body[7:9]) != 0xffff {
				t.Fatalf("失败返材弹窗或非背包删除标记不符：%x", packet.Body)
			}
			ackSeen = true
		}
		if packet.Header.MsgID == 14 && packet.Body[0] == 3 {
			if ackSeen || binary.LittleEndian.Uint16(packet.Body[3:5]) != 20 || binary.LittleEndian.Uint32(packet.Body[5:9]) != ^uint32(0) {
				t.Fatal("真正穿戴碎装没有在结果前清除")
			}
			wornCleared = true
		}
		if packet.Header.MsgID == 14 && packet.Body[0] == 0 {
			count := int(binary.LittleEndian.Uint16(packet.Body[1:3]))
			for i := 0; i < count; i++ {
				offset := 3 + i*currentItemListEntryWireSize
				if binary.LittleEndian.Uint16(packet.Body[offset:offset+2]) == 20 {
					t.Fatal("同号背包装备被重新发送，会让客户端误报获得")
				}
			}
		}
	}
	after, _, _ := g.Inventory.Load(ctx, "2")
	afterEq, _, _ := g.Equipment.Load(ctx, "2")
	if !ackSeen || !wornCleared || !reflect.DeepEqual(untouched, after.Slots["0:20"]) || after.Slots["0:6"].Count != 1 || len(afterEq.Entries) != 0 {
		t.Fatal("碎装、材料扣除、返材窗口或同号背包保护有误")
	}
}
