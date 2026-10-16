package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 使用真实PVF和隔离内存仓储核对五种无耐久部位，不操作玩家资产。
func TestChronicleRealPVFAllNonDurableSlotsAndSixAuras(t *testing.T) {
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
	wanted := map[string]byte{"amulet": 19, "wrist": 20, "ring": 21, "support": 22, "magic stone": 23}
	selected := map[string]uint32{"ring": 100322403, "magic stone": 100352903}
	ids := make([]uint32, 0)
	for id, ref := range items.itemRefs {
		if ref.kind == dungeonDropItemEquipment && strings.Contains(strings.ToLower(ref.path), "character/common/") {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	identity := regexp.MustCompile(`(?s)\[character item check\]\s*14\s+4\s`)
	for _, id := range ids {
		if len(selected) == len(wanted) {
			break
		}
		ref := items.itemRefs[id]
		file := cleanDungeonDropPath(ref.path)
		if !strings.HasPrefix(file, "equipment/") {
			file = "equipment/" + file
		}
		if !strings.Contains(file, "/amulet/") && !strings.Contains(file, "/wrist/") && !strings.Contains(file, "/support/") {
			continue
		}
		text, err := items.source.ReadText(file)
		if err != nil || !identity.MatchString(text) {
			continue
		}
		doc, err := parseDungeonCardPVFDocument(items.source, file)
		if err != nil {
			t.Fatal(err)
		}
		rarity, _ := doc.Int("rarity")
		typ, _ := doc.Text("equipment type")
		typ = strings.ToLower(strings.Trim(strings.TrimSpace(typ), "`[]"))
		if rarity == 5 {
			if _, ok := wanted[typ]; ok && selected[typ] == 0 {
				selected[typ] = id
			}
		}
	}
	if len(selected) != len(wanted) {
		t.Fatalf("真实PVF缺少对应职业的无耐久部位样本：%v", selected)
	}
	for _, typ := range []string{"amulet", "wrist", "ring", "support", "magic stone"} {
		id := selected[typ]
		equipment, err := items.currentChronicleCatalog().Equipment(int64(id))
		if err != nil || equipment.Durability != 0 || equipment.TypeID != wanted[typ] {
			t.Fatalf("真实无耐久装备被拒绝：类型=%s ID=%d 目录=%+v 错误=%v", typ, id, equipment, err)
		}
		t.Logf("真实样本：%s ID=%d %s", typ, id, equipment.Path)
		for _, aura := range []int64{1254, 1255, 1256, 10090758, 10090759, 10090760} {
			t.Run(fmt.Sprintf("%s/%d", typ, aura), func(t *testing.T) {
				s, session, conn, g := chronicleFixture(t, chronicleFixtureSource(), aura)
				s.pvfItemCatalog = items
				ctx := context.Background()
				inv, _, err := g.Inventory.Load(ctx, "2")
				if err != nil {
					t.Fatal(err)
				}
				target := inv.Slots["0:30"]
				target.ItemID = int64(id)
				target.RawEntry = nil
				target.Extra["durability"] = "0"
				inv.Slots["0:30"] = target
				if err := g.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
				body := chronicleBody(uint32(aura), 9)
				binary.LittleEndian.PutUint32(body[9:], id)
				if err := s.handleCurrentChronicleRefine(session, body); err != nil {
					t.Fatal(err)
				}
				ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				if len(ack.Body) != 10 || ack.Body[0] != 1 || ack.Body[5] != 1 || len(rest) == 0 {
					t.Fatalf("合法无耐久精炼未成功或未刷新：%x", ack.Body)
				}
				inv, _, err = g.Inventory.Load(ctx, "2")
				if err != nil || inv.Slots["0:65"].Count != 4 {
					t.Fatalf("气息扣除不符：%v", err)
				}
				target = inv.Slots["0:30"]
				state, err := currentChronicleItemState(target)
				if err != nil || len(state.Options) != 1 || state.Options[0].EquipmentType != wanted[typ] || state.Options[0].Number != 9 || state.Durability != 0 {
					t.Fatalf("无耐久装备属性未保存：%+v %v", state, err)
				}
				encoded, _ := json.Marshal(target)
				if err := json.Unmarshal(encoded, &target); err != nil {
					t.Fatal(err)
				}
				cold := currentItemListEntryFromStack(0, 30, target)
				if cold.data[currentChronicleOptionsOffset] != 1 || len(currentChronicleMode1Vector(target.Extra, cold)) != 8 {
					t.Fatal("冷读取未恢复无耐久装备精炼属性")
				}
			})
		}
	}
}
