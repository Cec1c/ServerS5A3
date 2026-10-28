package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 用真实PVF分别走三类提取的完整提交及发包路径；不连接玩家库，不把发包断言当作界面验收。
func TestRealPVFExpertNativeExtractionChangedSlots(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要匹配的真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	jobs, err := dnfexpertjob.Load(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	const equipmentID = int64(100050285)
	metadata, err := jobs.Equipment(equipmentID)
	if err != nil {
		t.Fatal(err)
	}
	metadata.State = currentExpertJobEquipmentState(dnfrepo.ItemStack{ItemID: equipmentID, Count: 1})
	for _, kind := range []byte{dnfexpertjob.EnchanterType, dnfexpertjob.AlchemistType, dnfexpertjob.DollControllerType} {
		t.Run(fmt.Sprintf("副职业%d", kind), func(t *testing.T) {
			config, ok := jobs.Config(kind)
			if !ok {
				t.Fatal("缺少真实副职业配置")
			}
			ids := make([]int64, 0, len(config.Extractors))
			for id := range config.Extractors {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			extractorID := int64(0)
			for _, id := range ids {
				if plan, e := jobs.PlanExtraction(kind, 100, id, metadata, func(int) int { return 0 }); e == nil && len(plan.Materials) > 0 {
					extractorID = id
					break
				}
			}
			if extractorID == 0 {
				t.Fatal("实际PVF没有适用于样本道具的提取器")
			}
			repos := mustCurrentNPCShopRepositories(t, 1000, nil)
			character, _, _ := repos.Character.Load(ctx, "19")
			character.Name = "原生提取"
			character.Stats["expert_job_type"] = int64(kind)
			character.Stats["expert_job_exp"] = 100
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			inventory, _, _ := repos.Inventory.Load(ctx, "19")
			inventory.Slots = map[string]dnfrepo.ItemStack{"0:76": {ItemID: extractorID, Count: 1}, "0:70": {ItemID: equipmentID, Count: 1}}
			for _, slot := range []int16{10, 12, 13, 11} {
				inventory.Slots[currentCeraShopInventorySlotKey(0, slot)] = dnfrepo.ItemStack{ItemID: equipmentID, Count: 1}
			}
			if err := repos.Inventory.Save(ctx, inventory); err != nil {
				t.Fatal(err)
			}
			if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
				t.Fatal(err)
			}
			service := currentDisjointTestService(repos, items)
			service.expertJobCatalog = jobs
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 19, accountID: "account-1", itemListMigrationsDone: true}
			for _, slot := range []int16{10, 12, 13, 11} {
				conn.write.Reset()
				body := []byte{kind, 76, 0, 0, byte(slot), 0}
				if err := service.handleCurrentExpertJobExtraction(session, body); err != nil {
					t.Fatal(err)
				}
				packets := splitAllGameServerUpperPackets(t, conn.write.Bytes())
				if len(packets) < 2 || packets[0].Header.MsgID != 416 || packets[0].Header.Classification != 1 || packets[0].Body[0] != 1 {
					t.Fatalf("缺少原生成功应答：%x", conn.write.Bytes())
				}
				ack := packets[0].Body
				if ack[1] != 0 || int16(binary.LittleEndian.Uint16(ack[2:4])) != slot || len(ack) != 5+10*int(ack[4]) || ack[4] == 0 {
					t.Fatalf("提取应答游标或来源不匹配：%x", ack)
				}
				after, _, _ := repos.Inventory.Load(ctx, "19")
				account, _, _ := repos.AccountInventory.Load(ctx, "account-1")
				wanted := map[int16]bool{slot: true}
				for i := 0; i < int(ack[4]); i++ {
					wanted[int16(binary.LittleEndian.Uint16(ack[5+10*i:]))] = true
				}
				seen := map[int16]bool{}
				acks, updates := 0, 0
				for _, packet := range packets {
					if packet.Header.MsgID == 13 {
						t.Fatal("原生结果期间出现整容器替换")
					}
					if packet.Header.MsgID == 416 {
						acks++
					}
					if packet.Header.MsgID != 14 {
						continue
					}
					updates++
					wire := packet.Body
					if packet.Header.Classification != 0 || len(wire) < 3 || wire[0] != 0 || len(wire) != 3+int(binary.LittleEndian.Uint16(wire[1:3]))*currentItemListEntryWireSize {
						t.Fatalf("局部刷新布局错误：%x", wire)
					}
					for at := 3; at < len(wire); at += currentItemListEntryWireSize {
						row := wire[at : at+currentItemListEntryWireSize]
						s := int16(binary.LittleEndian.Uint16(row))
						if !wanted[s] || seen[s] {
							t.Fatalf("刷新了无关或重复槽位%d", s)
						}
						seen[s] = true
						key := currentCeraShopInventorySlotKey(0, s)
						stack := after.Slots[key]
						if dnfrepo.IsAccountSharedInventorySlot(0, s) {
							stack = account.Slots[key]
						}
						item, count := binary.LittleEndian.Uint32(row[2:6]), binary.LittleEndian.Uint32(row[6:10])
						if s == slot {
							if item != ^uint32(0) || count != 0 {
								t.Fatal("来源未以原生空槽清除")
							}
						} else if item != uint32(stack.ItemID) || count != uint32(stack.Count) {
							t.Fatal("局部刷新不是已提交仓储的绝对状态")
						}
					}
				}
				if acks != 1 || updates != 1 || len(seen) != len(wanted) || !reflect.DeepEqual(after.Slots["0:70"], inventory.Slots["0:70"]) || !reflect.DeepEqual(after.Slots["0:76"], inventory.Slots["0:76"]) {
					t.Fatalf("应答/局部更新或无关物品被改变：ACK=%d update=%d slots=%v", acks, updates, seen)
				}
			}
			before, _, _ := repos.Inventory.Load(ctx, "19")
			beforeAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			beforeChar, _, _ := repos.Character.Load(ctx, "19")
			conn.write.Reset()
			if err := service.handleCurrentExpertJobExtraction(session, []byte{kind, 76, 0, 0, 10, 0}); err != nil {
				t.Fatal(err)
			}
			packets := splitAllGameServerUpperPackets(t, conn.write.Bytes())
			after, _, _ := repos.Inventory.Load(ctx, "19")
			afterAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			afterChar, _, _ := repos.Character.Load(ctx, "19")
			if len(packets) != 1 || packets[0].Header.MsgID != 416 || packets[0].Body[0] != 0 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeAccount, afterAccount) || !reflect.DeepEqual(beforeChar, afterChar) {
				t.Fatal("重放已消耗来源改变资产或发出成功/刷新")
			}
		})
	}
}
