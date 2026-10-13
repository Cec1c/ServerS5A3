package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 现场17:14/17:15的op21正文：item=20082、wire_count=0、shop=3、actor=4。
// 只使用当前真实PVF和内存仓储，不触碰玩家库存或金币。
func TestRealPVFObservedEquipmentPurchaseUsesQuickSlotFallback(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("设置 DNFBRIDGE_REAL_PVF_SMOKE 后复核现场装备购买")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	shop, err := newCurrentNPCShopCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	pricing, err := resolveCurrentNPCShopActorPricing(shop, items, 20082, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("现场装备20082：kind=%s path=%s buyable=%t buy_gold=%d", pricing.Definition.Kind, pricing.Definition.PVFPath, pricing.Buyable, pricing.BuyGold)
	if pricing.Definition.Kind != dungeonDropItemEquipment || !pricing.Buyable || pricing.MaterialExchange {
		t.Fatalf("现场装备定价不满足金币购买契约：%+v", pricing)
	}
	for mode := 0; mode < 3; mode++ {
		t.Run(strconv.Itoa(mode), func(t *testing.T) {
			slots := map[string]dnfrepo.ItemStack{}
			if mode > 0 {
				for slot := int16(9); slot <= 64; slot++ {
					slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
				}
			}
			if mode == 2 {
				for slot := int16(3); slot <= 8; slot++ {
					slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
				}
			}
			gold := pricing.BuyGold + 1000
			repositories := mustCurrentNPCShopRepositories(t, gold, slots)
			service := currentNPCShopTestService(repositories, shop, items)
			connection := &bufferConn{}
			session := &gameSession{conn: connection, connID: "现场装备满栏回落复核", selectedCharacterID: 19}
			if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketBuyItem), currentNPCShopBuyTestBody(20082, 0, 3, 4)); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
			inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
			character, _, _ := repositories.Character.Load(context.Background(), "19")
			if mode == 2 {
				if ack.Body[0] != 0 || len(rest) != 0 || character.Stats["gold"] != gold || !reflect.DeepEqual(inventory.Slots, slots) {
					t.Fatalf("两处皆满应拒绝且不扣款：ACK=%x gold=%d", ack.Body, character.Stats["gold"])
				}
				return
			}
			want := int16(9)
			if mode == 1 {
				want = 3
			}
			stack := inventory.Slots[currentDungeonPickupMainSlotKey(want)]
			if ack.Body[0] != 1 || stack.ItemID != 20082 || stack.Count != 1 || character.Stats["gold"] != 1000 {
				t.Fatalf("现场装备购买未按分类/快捷格落库：want=%d ACK=%x gold=%d stack=%+v", want, ack.Body, character.Stats["gold"], stack)
			}
			update, trailing := splitGameServerUpperPacket(t, rest)
			if update.Header.MsgID != 14 || len(trailing) != 0 || len(stack.RawEntry) != currentItemListEntryWireSize || !bytes.Contains(update.Body, stack.RawEntry) {
				t.Fatalf("成功购买未发送真实装备实例刷新：header=%+v trailing=%d raw_len=%d", update.Header, len(trailing), len(stack.RawEntry))
			}
		})
	}
}
