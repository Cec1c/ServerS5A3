package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"testing"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestNativeNPCBatchDiscardsPartialGrantBeforeContinuing(t *testing.T) {
	s, p, repos, trace, packets := nativeNPCBatchFixture(t, 2)
	source := s.pvfItemCatalog.source.(bridgePVFSource)
	// 第一件先生成无色，再因缺少附加产物定义失败；第二件仅生成无色。
	source["equipment/equipment.lst"] += "5001 `only_cube.equ`\n"
	source["equipment/only_cube.equ"] = "[equipment type] `[coat]`\n[rarity] 1\n[minimum level] 10\n[value] 300\n"
	delete(source, "stackable/additional.stk")
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = catalog
	bag, _, _ := repos.Inventory.Load(context.Background(), "19")
	bag.Slots["0:11"] = dnfrepo.ItemStack{ItemID: 5001, Count: 1}
	if err = repos.Inventory.Save(context.Background(), bag); err != nil {
		t.Fatal(err)
	}
	if handled, err := s.handleNativeNPCDisjointBatch(p, packets); !handled || err != nil {
		t.Fatal(handled, err)
	}
	if err = s.finishNativeResultDelivery(p, "单件产物失败"); err != nil {
		t.Fatal(err)
	}
	if got := nativeNPCDecodeACKs(t, s, p.conn.(*channelSceneWriteConn).write.Bytes()); !reflect.DeepEqual(got, []byte{0, 1}) {
		t.Fatal("失败后未继续或应答不符", got)
	}
	bag, _, _ = repos.Inventory.Load(context.Background(), "19")
	account, _, _ := repos.AccountInventory.Load(context.Background(), "account-1")
	if bag.Slots["0:10"].ItemID != 5000 {
		t.Fatal("失败单件的来源被消耗")
	}
	if _, exists := bag.Slots["0:11"]; exists {
		t.Fatal("后续有效来源未消耗")
	}
	if len(account.Slots) != 1 || account.Slots["0:358"].Count != 2 {
		t.Fatal("失败单件泄漏了临时产物", account.Slots)
	}
	if trace.transactions != 1 {
		t.Fatal(trace.transactions)
	}
}

func TestNativeNPCBatchCommittedAssetsSurviveShortWriteWithoutReplay(t *testing.T) {
	s, p, repos, trace, packets := nativeNPCBatchFixture(t, 24)
	p.conn = &channelSceneWriteConn{short: true}
	if handled, err := s.handleNativeNPCDisjointBatch(p, packets); !handled || err != nil {
		t.Fatal(handled, err)
	}
	if err := s.finishNativeResultDelivery(p, "提交后断线"); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("没有报告短写", err)
	}
	if p.nativeNPCDisjointReplay != nil || p.nativeResultDelivery != nil || !p.conn.(*channelSceneWriteConn).closed {
		t.Fatal("断线未清理会话结果")
	}
	before, _, _ := repos.AccountInventory.Load(context.Background(), "account-1")
	bag, _, _ := repos.Inventory.Load(context.Background(), "19")
	if len(bag.Slots) != 0 || before.Slots["0:358"].Count != 48 {
		t.Fatal("提交后断线丢失资产", bag.Slots, before.Slots)
	}
	if err := s.finishNativeResultDelivery(p, "重复清理"); err != nil {
		t.Fatal(err)
	}
	after, _, _ := repos.AccountInventory.Load(context.Background(), "account-1")
	if trace.transactions != 1 || !reflect.DeepEqual(before, after) {
		t.Fatal("清理触发重复结算")
	}
}

func TestNativeNPCBatchReducesSettlementWorkComparedWithPerItem(t *testing.T) {
	for _, batch := range []bool{false, true} {
		s, p, _, trace, packets := nativeNPCBatchFixture(t, 24)
		if batch {
			if handled, err := s.handleNativeNPCDisjointBatch(p, packets); !handled || err != nil {
				t.Fatal(handled, err)
			}
		} else {
			for _, packet := range packets {
				if err := s.prepareNativeResultDelivery(p, packet); err != nil {
					t.Fatal(err)
				}
				if err := s.handleGameStreamPacket(p, packet); err != nil {
					t.Fatal(err)
				}
			}
		}
		want := 24
		if batch {
			want = 1
		}
		if trace.transactions != want || trace.characterLoads != want || trace.inventoryLoads != want || trace.accountLoads != want || trace.inventorySaves != want || trace.accountSaves != want {
			t.Fatalf("批量=%v 结算调用数不符：%+v", batch, trace)
		}
		t.Logf("批量=%v：24条原生请求，资产事务及各资产读取/保存次数=%d；不作为数据库或游戏耗时基准", batch, want)
	}
}

func TestNativeNPCBatchRejectsOtherFramingAndLimitsPrefix(t *testing.T) {
	_, _, _, _, packets := nativeNPCBatchFixture(t, 128)
	if n := nativeNPCDisjointPrefix(packets); n != 127 {
		t.Fatal("件数保护失效", n)
	}
	upper := packets[0]
	upper.Kind = dnfproto.LatestGameStreamUpper
	notification := packets[0]
	notification.Data = append([]byte(nil), notification.Data...)
	notification.Data[0] = 0
	compact := nativeResultTestPacket(26, append(currentDisjointItemTestBody(10, 0, -1, 317), []byte{0x42, 0x32, 0x36, 1, 2, 10, 0, 11, 0}...))
	for _, packet := range []dnfproto.LatestGameStreamPacket{upper, notification, compact} {
		if _, _, ok := nativeNPCDisjointPacket(packet); ok {
			t.Fatal("其它原生/扩展形状误入合批")
		}
	}
}

func TestRealPVFNativeNPCBatchSettlement(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要匹配的真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{24, 127} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, p, repos, trace, packets := nativeNPCBatchFixture(t, count)
			s.pvfItemCatalog = catalog
			ctx := context.Background()
			character, _, _ := repos.Character.Load(ctx, "19")
			character.Stats["expert_job_type"] = 2
			character.Stats["expert_job_exp"] = 123
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			bag, _, _ := repos.Inventory.Load(ctx, "19")
			for slot := range count {
				id := int64(100300212)
				if slot%2 == 0 {
					id = 100050285
				}
				bag.Slots[fmt.Sprintf("0:%d", slot+10)] = dnfrepo.ItemStack{ItemID: id, Count: 1}
			}
			if err := repos.Inventory.Save(ctx, bag); err != nil {
				t.Fatal(err)
			}
			if handled, err := s.handleNativeNPCDisjointBatch(p, packets); !handled || err != nil {
				t.Fatal(handled, err)
			}
			if err := s.finishNativeResultDelivery(p, "真实PVF NPC批量"); err != nil {
				t.Fatal(err)
			}
			c := p.conn.(*channelSceneWriteConn)
			if len(c.writes) != 1 || trace.transactions != 1 || trace.inventorySaves != 1 {
				t.Fatal("真实资源仍拆分提交或写回", len(c.writes), trace.transactions)
			}
			gains := map[int64]int64{}
			wire := c.write.Bytes()
			acks := 0
			for len(wire) > 0 {
				packet, rest := splitGameServerUpperPacketWithHeader(t, wire, s.gameUpperHeaderSize())
				wire = rest
				if packet.Header.Classification != 1 || packet.Header.MsgID != 26 || len(packet.Body) < 7 || packet.Body[0] != 1 || int(binary.LittleEndian.Uint16(packet.Body[1:3])) != acks+10 {
					t.Fatal("真实来源或应答顺序改变", acks, packet)
				}
				for row := 0; row < int(packet.Body[6]); row++ {
					at := 7 + row*10
					gains[int64(binary.LittleEndian.Uint32(packet.Body[at+2:at+6]))] += int64(binary.LittleEndian.Uint32(packet.Body[at+6 : at+10]))
				}
				acks++
			}
			if acks != count || len(gains) == 0 {
				t.Fatal("应答或产物丢失", acks, gains)
			}
			bag, _, _ = repos.Inventory.Load(ctx, "19")
			account, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			actual := map[int64]int64{}
			for _, stack := range bag.Slots {
				actual[stack.ItemID] += stack.Count
			}
			for _, stack := range account.Slots {
				actual[stack.ItemID] += stack.Count
			}
			if !reflect.DeepEqual(gains, actual) {
				t.Fatal("真实产物总量与应答不符", gains, actual)
			}
			after, _, _ := repos.Character.Load(ctx, "19")
			if !reflect.DeepEqual(after, character) {
				t.Fatal("NPC分解改变了角色或副职业经验")
			}
			t.Logf("真实PVF：%d件、单事务、%d条ACK、产物/资产一致；NPC不授予副职业经验", count, acks)
		})
	}
}
