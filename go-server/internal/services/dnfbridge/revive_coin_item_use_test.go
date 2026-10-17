package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func reviveCoinItemUseFixture(t *testing.T, count int64) (*Service, dnfrepo.Group, *gameSession, *bufferConn) {
	t.Helper()
	repos, _ := mustCurrentExperienceItemTestAssets(t, map[string]dnfrepo.ItemStack{
		"0:68": {ItemID: 42, Count: count},
		"0:1":  {ItemID: 1, Count: 7},
	}, currentExperienceItemTestPoints())
	source := currentExperienceItemTestPVF()
	source["stackable/stackable.lst"] += "42 `cash/coin_general.stk`\n"
	source["stackable/cash/coin_general.stk"] = "[name] `复活币`\n[stackable type] `[waste]` 0\n"
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	service := currentExperienceItemTestService(repos, catalog)
	connection := &bufferConn{}
	return service, repos, &gameSession{conn: connection, connID: "复活币使用回归", selectedCharacterID: 19}, connection
}

// 真实报错：2026-10-15 20:59 使用物品42走op30，却被当作技能书拒绝。
func TestCurrentReviveCoinItemUseOp30ConsumesOneAndRefreshesWallet(t *testing.T) {
	for _, count := range []int64{1, 3} {
		for _, trailer := range []bool{false, true} {
			service, repos, session, conn := reviveCoinItemUseFixture(t, count)
			ctx := context.Background()
			before, _, _ := repos.Character.Load(ctx, "19")
			beforeSkill, _, _ := repos.Skill.Load(ctx, "19")
			body := currentExperienceItemTestBody(68)
			if trailer {
				body = append(body, 0, 0, 0, 0)
			}
			if err := service.handleCurrentExperienceItemUse(session, body); err != nil {
				t.Fatal(err)
			}
			packets := currentExperienceItemWirePackets(t, conn)
			wantAck := []byte{1, 19, 0, 0xff, 0, 0, 0, 0, 0, 0, 0, 0}
			if len(packets) != 2 || packets[0].Header.MsgID != 30 || packets[0].Header.Classification != 1 || !bytes.Equal(packets[0].Body, wantAck) {
				t.Fatalf("物品42应成功并只刷新库存: count=%d trailer=%t packets=%+v", count, trailer, packets)
			}
			update := packets[1]
			rowSize := currentItemListEntrySizeForType(0)
			if update.Header.MsgID != 14 || update.Header.Classification != 0 || len(update.Body) != 3+2*rowSize || update.Body[0] != 0 || binary.LittleEndian.Uint16(update.Body[1:3]) != 2 {
				t.Fatalf("背包增量缺少源格与钱包格: %+v", update)
			}
			for index, slot := range []uint16{68, 1} {
				row := update.Body[3+index*rowSize:]
				wantID, wantCount := uint32(42), uint32(count-1)
				if slot == 1 {
					wantID, wantCount = 1, 8
				} else if count == 1 {
					wantID = ^uint32(0)
				}
				if binary.LittleEndian.Uint16(row) != slot || binary.LittleEndian.Uint32(row[2:]) != wantID || binary.LittleEndian.Uint32(row[6:]) != wantCount {
					t.Fatalf("增量行不匹配: slot=%d row=%x", slot, row[:10])
				}
			}
			inventory, _, _ := repos.Inventory.Load(ctx, "19")
			if inventory.Slots["0:68"].Count != count-1 || inventory.Slots["0:1"].Count != 8 {
				t.Fatalf("落库数量不匹配: %+v", inventory.Slots)
			}
			after, _, _ := repos.Character.Load(ctx, "19")
			afterSkill, _, _ := repos.Skill.Load(ctx, "19")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeSkill, afterSkill) {
				t.Fatal("使用复活币不应修改等级、经验或技能加点")
			}
		}
	}
}

func TestRealPVFReviveCoinItemUseOp30(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要设置真实PVF路径")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := catalog.ResolveItem(42)
	if err != nil || definition.Kind != dungeonDropItemStackable || definition.PVFPath != "stackable/cash/coin_general.stk" || normalizeMagicBoxPVFType(definition.StackableType) != "waste" {
		t.Fatalf("当前PVF物品42身份不匹配: %+v err=%v", definition, err)
	}
	service, repos, session, conn := reviveCoinItemUseFixture(t, 3)
	service.pvfItemCatalog = catalog
	service.initialSPTable, service.initialTPTable = nil, nil
	if err := service.handleCurrentExperienceItemUse(session, currentExperienceItemTestBody(68)); err != nil {
		t.Fatal(err)
	}
	packets := currentExperienceItemWirePackets(t, conn)
	if len(packets) != 2 || packets[0].Body[0] != 1 || packets[1].Header.MsgID != 14 {
		t.Fatalf("真实PVF使用失败: %+v", packets)
	}
	inventory, _, _ := repos.Inventory.Load(context.Background(), "19")
	if inventory.Slots["0:68"].Count != 2 || inventory.Slots["0:1"].Count != 8 {
		t.Fatalf("真实PVF发放数量不匹配: %+v", inventory.Slots)
	}
	t.Logf("物品42: path=%s type=%s；仅隔离内存仓储消费一件，钱包7→8，未触碰玩家数据", definition.PVFPath, definition.StackableType)
}

func TestCurrentReviveCoinItemUseWrongClassDoesNotConsume(t *testing.T) {
	service, repos, session, conn := reviveCoinItemUseFixture(t, 1)
	module := experienceItemGameplayModule()
	if err := module.UpperHandlers[30](service, session, gameplayRequest{Opcode: 30, Classification: 0, Body: currentExperienceItemTestBody(68)}); err != nil {
		t.Fatal(err)
	}
	inventory, _, _ := repos.Inventory.Load(context.Background(), "19")
	if inventory.Slots["0:68"].Count != 1 || inventory.Slots["0:1"].Count != 7 || conn.write.Len() != 0 {
		t.Fatal("错误协议分类导致资产变化或发送成功包")
	}
}
