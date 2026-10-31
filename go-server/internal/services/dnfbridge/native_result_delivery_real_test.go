package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"testing"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 四种副职业均走真实PVF、原生逐件请求、独立事务和原有应答布局。
// 连续写回只改变连接提交次数，不改变产物、经验或来源槽应答数量。
func TestRealPVFNativeResultDeliveryFourProfessions(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要匹配的真实PVF")
	}
	t.Setenv("DNFBRIDGE_NATIVE_RESULT_DELIVERY", "on")
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
	for _, kind := range []byte{1, 2, 3, 4} {
		t.Run(fmt.Sprintf("副职业%d", kind), func(t *testing.T) {
			equipmentID := int64(100050285)
			if kind == 3 {
				equipmentID = 100300212
			}
			extractorID := int64(0)
			if kind != 3 {
				metadata, err := jobs.Equipment(equipmentID)
				if err != nil {
					t.Fatal(err)
				}
				metadata.State = currentExpertJobEquipmentState(dnfrepo.ItemStack{ItemID: equipmentID, Count: 1})
				config, ok := jobs.Config(kind)
				if !ok {
					t.Fatal("副职业配置缺失")
				}
				var ids []int64
				for id := range config.Extractors {
					ids = append(ids, id)
				}
				sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
				for _, id := range ids {
					if plan, e := jobs.PlanExtraction(kind, 100, id, metadata, func(int) int { return 0 }); e == nil && len(plan.Materials) > 0 {
						extractorID = id
						break
					}
				}
				if extractorID == 0 {
					t.Fatal("真实PVF没有样本适用提取器")
				}
			}
			repos := mustCurrentNPCShopRepositories(t, 1000, nil)
			character, _, _ := repos.Character.Load(ctx, "19")
			character.Name = "连续结果"
			character.Stats["expert_job_type"] = int64(kind)
			character.Stats["expert_job_exp"] = 100
			character.Stats[currentExpertJobMachineGradeStat] = 1
			character.Stats[currentExpertJobMachineEnduranceStat] = 300
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			inventory, _, _ := repos.Inventory.Load(ctx, "19")
			inventory.Slots = map[string]dnfrepo.ItemStack{}
			for slot := int16(10); slot < 14; slot++ {
				inventory.Slots[currentCeraShopInventorySlotKey(0, slot)] = dnfrepo.ItemStack{ItemID: equipmentID, Count: 1}
			}
			if kind != 3 {
				inventory.Slots["0:76"] = dnfrepo.ItemStack{ItemID: extractorID, Count: 1}
			}
			if err := repos.Inventory.Save(ctx, inventory); err != nil {
				t.Fatal(err)
			}
			if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
				t.Fatal(err)
			}
			s := currentDisjointTestService(repos, items)
			warehouseWrites := &extractionWarehouseWriteTrace{CharacterSettlementUnitOfWork: repos.CharacterSettlement}
			repos.CharacterSettlement = warehouseWrites
			// 生产仓储提供者在创建服务时捕获组值，绑定本轮观察包装。
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			s.options.currentClientVersion = "official-24.9"
			s.expertJobCatalog = jobs
			c := &channelSceneWriteConn{}
			p := &gameSession{conn: c, selectedCharacterID: 19, accountID: "account-1", itemListMigrationsDone: true}
			opcode := uint16(416)
			if kind == 3 {
				opcode = 235
			}
			attendance := buildCurrentOrdinaryAttendanceBody(onlineevent.OrdinarySnapshot{Days: 15, Claimed: 11, CanClaim: true})
			online := buildCurrentPCRoomTimePointBody(onlineevent.PCRoomTimePointSnapshot{Seconds: 13680, Total: 11, DailyMask: 15, PeriodMask: 3})
			for slot := byte(10); slot < 14; slot++ {
				body := []byte{kind, 76, 0, 0, slot, 0}
				if kind == 3 {
					body = []byte{19, 0, slot, 0, 0}
				}
				packet := nativeResultTestPacket(opcode, body)
				if err := s.prepareNativeResultDelivery(p, packet); err != nil {
					t.Fatal(err)
				}
				if err := s.handleLegacyGamePacket(p, packet.Data); err != nil {
					t.Fatal(err)
				}
				if slot == 10 {
					if err := s.sendGameUpperRawClass(p, 1386, attendance, 0); err != nil {
						t.Fatal(err)
					}
				}
				if slot == 11 {
					if err := s.sendGameUpperRawClass(p, currentPCRoomTimePointStateID, online, 0); err != nil {
						t.Fatal(err)
					}
				}
			}
			if len(c.writes) != 0 {
				t.Fatal("连续请求处理期间提前写出结果")
			}
			if err := s.finishNativeResultDelivery(p, "真实PVF专项"); err != nil {
				t.Fatal(err)
			}
			if len(c.writes) != 1 {
				t.Fatal("连续结果没有集中写回", len(c.writes))
			}
			packets := splitAllGameServerUpperPackets(t, c.write.Bytes())
			ackCount := 0
			statusCount := 0
			gains := map[uint32]int64{}
			for _, packet := range packets {
				if packet.Header.MsgID == 1386 || packet.Header.MsgID == currentPCRoomTimePointStateID {
					want := attendance
					if packet.Header.MsgID == currentPCRoomTimePointStateID {
						want = online
					}
					if packet.Header.Classification != 0 || !bytes.Equal(packet.Body, want) {
						t.Fatal("定时状态的内容或分类改变", packet)
					}
					statusCount++
				}
				if packet.Header.MsgID != opcode || packet.Header.Classification != 1 {
					continue
				}
				ackCount++
				body := packet.Body
				if len(body) < 5 || body[0] != 1 {
					t.Fatalf("原生逐件应答失败，职业%d：%x", kind, body)
				}
				offset, count := 5, int(body[4])
				if kind == 3 {
					offset, count = 7, int(body[6])
				}
				for i := 0; i < count; i++ {
					at := offset + 10*i
					gains[binary.LittleEndian.Uint32(body[at+2:at+6])] += int64(binary.LittleEndian.Uint32(body[at+6 : at+10]))
				}
			}
			if ackCount != 4 {
				t.Fatal("原生剩余件数所需应答数量改变", ackCount)
			}
			if statusCount != 2 {
				t.Fatal("签到或在线奖励状态丢失、重复", statusCount)
			}
			after, _, _ := repos.Inventory.Load(ctx, "19")
			account, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			for slot := int16(10); slot < 14; slot++ {
				if _, ok := after.Slots[currentCeraShopInventorySlotKey(0, slot)]; ok {
					t.Fatal("来源未消耗", slot)
				}
			}
			for item, want := range gains {
				got := int64(0)
				for _, stack := range after.Slots {
					if stack.ItemID == int64(item) {
						got += stack.Count
					}
				}
				for _, stack := range account.Slots {
					if stack.ItemID == int64(item) {
						got += stack.Count
					}
				}
				if got != want {
					t.Fatal("ACK产物与实际资产不一致", kind, item, got, want)
				}
			}
			if len(gains) == 0 {
				t.Fatal("专项没有实际产物")
			}
			if kind != 3 {
				sharedReward := false
				for item := range gains {
					if _, ok := currentDisjointWarehouseFixedSlots[item]; ok {
						sharedReward = true
					}
				}
				if !sharedReward && warehouseWrites.saves != 0 {
					t.Fatal("产物未入账号仓库仍重复保存仓库", warehouseWrites.saves)
				}
				if sharedReward && warehouseWrites.saves == 0 {
					t.Fatal("账号仓库产物漏保存")
				}
			}
		})
	}
}
