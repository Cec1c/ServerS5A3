package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestEquipmentContainerQualityProjection(t *testing.T) {
	for _, seed := range []uint32{85180097, currentEquipmentTopQualitySeed} {
		for _, rawOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("seed_%d_raw_%t", seed, rawOnly), func(t *testing.T) {
				raw := make([]byte, currentItemListEntryWireSize)
				binary.LittleEndian.PutUint32(raw[2:6], 9001)
				binary.LittleEndian.PutUint32(raw[6:10], seed)
				raw[10], raw[13] = 12, 1
				binary.LittleEndian.PutUint16(raw[11:13], 28)
				binary.LittleEndian.PutUint32(raw[14:18], 7654321)
				extra := map[string]string{"item_kind": "equipment"}
				if !rawOnly {
					extra["quality_seed"] = fmt.Sprint(seed)
					binary.LittleEndian.PutUint32(raw[6:10], 1) // 缓存旧值不能覆盖明确品级。
				}
				stack := dnfrepo.ItemStack{ItemID: 9001, Count: 1, RawEntry: raw, Extra: extra}
				before := append([]byte(nil), raw...)
				for _, list := range []byte{0, 2, 3, 12} {
					entry := currentItemListEntryFromStack(list, 9, stack)
					if got := binary.LittleEndian.Uint32(entry.data[6:10]); got != seed {
						t.Errorf("容器%d品级变成%d，原值%d", list, got, seed)
					}
					if !bytes.Equal(entry.data[10:18], raw[10:18]) {
						t.Fatalf("容器%d改变了强化、耐久、封装或实例字段", list)
					}
				}
				if !bytes.Equal(raw, before) || stack.Count != 1 {
					t.Fatal("发包投影修改了持久化行或装备件数")
				}
			})
		}
	}
}

func TestEquipmentContainerQualityKeepsOtherItemSemantics(t *testing.T) {
	for _, list := range []byte{0, 2, 12} {
		stack := dnfrepo.ItemStack{ItemID: 3227, Count: 23, Extra: map[string]string{"item_kind": "stackable", "amount_or_count": "99"}}
		if got := currentItemListStackAmount(list, stack); got != 23 {
			t.Fatalf("容器%d堆叠物数量%d，期望23", list, got)
		}
		avatar := dnfrepo.ItemStack{ItemID: 40734, Count: 1, Extra: map[string]string{"item_kind": "avatar", "quality_seed": "999999998"}}
		if got := currentItemListStackAmount(list, avatar); got != 0 {
			t.Fatalf("容器%d永久装扮期限被当作品级：%d", list, got)
		}
	}
	pet := dnfrepo.ItemStack{ItemID: 5001, Count: 1, Extra: map[string]string{"serial": "41", "quality_seed": "999999998"}}
	if got := currentItemListStackAmount(7, pet); got != 41 {
		t.Fatalf("宠物序号变成%d", got)
	}
}

func TestEquipmentContainerQualityMoveRoundTrip(t *testing.T) {
	for _, list := range []byte{2, 12} {
		t.Run(fmt.Sprint(list), func(t *testing.T) {
			ctx := context.Background()
			f := newRefreshCostFixture(t)
			f.warmUp(t)
			raw := make([]byte, currentItemListEntryWireSize)
			binary.LittleEndian.PutUint16(raw[:2], 9)
			binary.LittleEndian.PutUint32(raw[2:6], 9001)
			binary.LittleEndian.PutUint32(raw[6:10], currentEquipmentTopQualitySeed)
			raw[10] = 12
			binary.LittleEndian.PutUint16(raw[11:13], 28)
			stack := dnfrepo.ItemStack{ItemID: 9001, Count: 1, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "quality_seed": "999999998", "durability": "28", "max_durability": "57"}}
			inv, _, _ := f.group.Inventory.Load(ctx, "19")
			inv.Slots = map[string]dnfrepo.ItemStack{"0:9": stack}
			inv.Warehouse = map[string]dnfrepo.ItemStack{}
			if err := f.group.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			acct, _, _ := f.group.Account.Load(ctx, "dnf:1")
			if acct.Metadata == nil {
				acct.Metadata = map[string]string{}
			}
			acct.Metadata["account_cargo_created"], acct.Metadata["account_cargo_level"] = "true", "8"
			if err := f.group.Account.Save(ctx, acct); err != nil {
				t.Fatal(err)
			}
			for _, endpoints := range [][4]int{{0, 9, int(list), 0}, {int(list), 0, 0, 9}} {
				body := make([]byte, 28)
				body[0], body[11] = byte(endpoints[0]), byte(endpoints[2])
				binary.LittleEndian.PutUint16(body[1:3], uint16(endpoints[1]))
				binary.LittleEndian.PutUint32(body[3:7], 9001)
				binary.LittleEndian.PutUint32(body[7:11], 1)
				binary.LittleEndian.PutUint16(body[12:14], uint16(endpoints[3]))
				binary.LittleEndian.PutUint32(body[22:26], ^uint32(0))
				result, err := dnfinventory.NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketMoveItemspace), Body: body, AccountID: "dnf:1", SelectedCharacterID: 19, Repositories: f.group})
				if err != nil || !result.ResponseAllowed || len(result.UpperResponses) == 0 || len(result.UpperResponses[0].Body) == 0 || result.UpperResponses[0].Body[0] != 1 {
					t.Fatalf("移动失败：%+v %v", result, err)
				}
				f.buffer.write.Reset()
				if err := f.service.sendAlignedUpperResponses(f.session, result); err != nil {
					t.Fatal(err)
				}
				wire := f.buffer.write.Bytes()
				seen := false
				for len(wire) > 0 {
					packet, rest := splitGameServerUpperPacketWithHeader(t, wire, dnfproto.GameServerUpperHeaderSize16)
					wire = rest
					if packet.Header.Classification != 0 || (packet.Header.MsgID != 13 && packet.Header.MsgID != 14) || packet.Body[0] != byte(endpoints[2]) {
						continue
					}
					offset := 3
					if packet.Header.MsgID == 13 {
						offset = 5
						if packet.Body[0] == 12 {
							offset = 9
						}
					}
					for ; offset+currentItemListEntryWireSize <= len(packet.Body); offset += currentItemListEntryWireSize {
						row := packet.Body[offset : offset+currentItemListEntryWireSize]
						if binary.LittleEndian.Uint32(row[2:6]) != 9001 {
							continue
						}
						seen = true
						if binary.LittleEndian.Uint32(row[6:10]) != currentEquipmentTopQualitySeed || row[10] != 12 || binary.LittleEndian.Uint16(row[11:13]) != 28 {
							t.Fatalf("移动后装备显示变化：%x", row[:18])
						}
					}
				}
				if !seen {
					t.Fatal("没有发送目标容器装备刷新")
				}
				snapshot, _, _, ok := f.service.buildCurrentItemListBodyForSession(ctx, f.session, byte(endpoints[2]))
				if !ok {
					t.Fatal("重开容器未生成快照")
				}
				found := false
				offset := 5
				if snapshot[0] == 12 {
					offset = 9
				}
				for ; offset+currentItemListEntryWireSize <= len(snapshot); offset += currentItemListEntryWireSize {
					row := snapshot[offset : offset+currentItemListEntryWireSize]
					if binary.LittleEndian.Uint32(row[2:6]) == 9001 {
						found = true
						if binary.LittleEndian.Uint32(row[6:10]) != currentEquipmentTopQualitySeed {
							t.Fatalf("重开容器%d后品级改变", endpoints[2])
						}
					}
				}
				if !found {
					t.Fatal("重开容器遗漏装备")
				}
			}
			after, _, _ := f.group.Inventory.Load(ctx, "19")
			if got := after.Slots["0:9"]; got.Count != 1 || !reflect.DeepEqual(got.Extra, stack.Extra) || !bytes.Equal(got.RawEntry[2:], raw[2:]) {
				t.Fatalf("往返改变了装备实例：%+v", got)
			}
		})
	}
}
