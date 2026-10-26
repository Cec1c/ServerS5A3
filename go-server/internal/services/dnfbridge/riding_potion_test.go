package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRidingPotionDefinitionRejectsOtherTransformationMechanisms(t *testing.T) {
	source := "[stackable type] `[waste]` 0\n[action type] `[riding]` 107000902 300000\n[/action type]\n[action usable place] `[dungeon]`\n[/action usable place]\n[action usable dungeon] 80 81\n[/action usable dungeon]\n[cool time] 10000\n[need material] 77 2\n[/need material]\n"
	for _, kind := range []string{"valid", "disguise", "ridable", "celebrate", "bad_duration", "wrong_place", "extra_condition", "odd_material"} {
		t.Run(kind, func(t *testing.T) {
			text := source
			switch kind {
			case "disguise":
				text = strings.Replace(text, "`[waste]`", "`[disguise]`", 1)
			case "ridable":
				text = strings.Replace(text, "`[riding]`", "`[ridable item]`", 1)
			case "celebrate":
				text = strings.Replace(text, "`[riding]`", "`[celebrate big transition]`", 1)
			case "bad_duration":
				text = strings.Replace(text, "300000", "0", 1)
			case "wrong_place":
				text = strings.Replace(text, "`[dungeon]`", "`[village]`", 1)
			case "extra_condition":
				text += "[action usable condition] `[guildmember]`\n[/action usable condition]\n"
			case "odd_material":
				text = strings.Replace(text, "77 2", "77", 1)
			}
			doc, err := dnfpvf.Parse("stackable/test.stk", text)
			if err != nil {
				t.Fatal(err)
			}
			d, err := currentRidingPotionDefinition(88, "stackable/test.stk", doc)
			if kind == "valid" {
				if err != nil || d.FormID != 107000902 || d.DurationMS != 300000 || d.Materials[77] != 2 || d.CoolTimeMS != 10000 {
					t.Fatalf("解析=%+v/%v", d, err)
				}
			} else if err == nil {
				t.Fatal("不同机制或缺损配置被接受")
			}
		})
	}
}

func TestRidingPotionDefinitionsRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要当前真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	list, err := archive.ReadText("stackable/stackable.lst")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dnfpvf.Parse("stackable/stackable.lst", list)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	materials := 0
	egene := 0
	excluded := 0
	// 这些版本另有每日次数或其它内容限制，不并入本轮普通动作99修复。
	extraRules := map[int64]string{10158851: "total usable count", 10159431: "total usable count", 10160487: "total usable count", 10160559: "total usable count", 490008079: "total usable count", 490006455: "gift", 490008316: "gift", 490008405: "gift", 490010019: "ban redeemitem"}
	for _, entry := range dnfpvf.ParseList(doc) {
		rel := entry.Path
		if !strings.HasPrefix(rel, "stackable/") {
			rel = "stackable/" + rel
		}
		text, err := archive.ReadText(rel)
		if err != nil {
			continue
		}
		if !strings.Contains(text, "`[riding]`") {
			continue
		}
		t.Run("item_"+strings.ReplaceAll(rel, "/", "_"), func(t *testing.T) {
			d, err := dnfpvf.Parse(rel, text)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := currentRidingPotionDefinition(entry.ID, rel, d)
			if rule, ok := extraRules[entry.ID]; ok {
				present := false
				if rule == "total usable count" {
					tokens, found := d.Section(rule)
					present = found && len(tokens) > 0
				} else {
					for _, content := range d.Texts("impossible contents") {
						present = present || content == rule
					}
				}
				if err == nil || !present {
					t.Fatalf("特殊规则未按已核对边界拒绝：%s / %v", rule, err)
				}
				excluded++
				return
			}
			if err != nil {
				t.Fatalf("同类配置被拒绝：%v", err)
			}
			count++
			if len(resolved.Materials) > 0 {
				materials++
			}
			if resolved.FormID == 107000902 {
				egene++
			}
		})
	}
	if count != 57 || excluded != 9 || egene == 0 {
		t.Fatalf("实际PVF范围变化：接入%d，额外条件%d，厄伽勒%d，需要重新核对", count, excluded, egene)
	}
	t.Logf("已验证实际索引的同类药水%d件，厄伽勒形态%d件，需材料%d件", count, egene, materials)
}

func TestRidingPotionDispatchAndFailuresRealPVF(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("需要当前真实PVF")
	}
	for _, kind := range []string{"last_potion", "materials", "town", "wrong_map", "expired", "reserved", "missing_materials"} {
		t.Run(kind, func(t *testing.T) {
			s, p, g := guildMileageFixture(t)
			ctx := context.Background()
			catalog, err := newPVFDungeonDropCatalog(s.initialEquipmentArchive)
			if err != nil {
				t.Fatal(err)
			}
			s.pvfItemCatalog = catalog
			c, _, _ := g.Character.Load(ctx, "29")
			c.Level = 90
			if err := g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			id := int64(490009860)
			if kind == "materials" || kind == "missing_materials" {
				id = 490005150
			}
			bag := repo.InventoryRecord{CharacterID: "29", Slots: map[string]repo.ItemStack{"0:4": {ItemID: id, Count: 1, Extra: map[string]string{"item_kind": "stackable"}}}}
			if kind == "materials" {
				bag.Slots["0:5"] = repo.ItemStack{ItemID: 490005151, Count: 2}
			}
			if kind == "expired" {
				stack := bag.Slots["0:4"]
				stack.ExpireAt = s.gameplayNow().Add(-time.Second)
				bag.Slots["0:4"] = stack
			}
			if err := g.Inventory.Save(ctx, bag); err != nil {
				t.Fatal(err)
			}
			p.dungeon.runtime = &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 80}, Character: c, AccountID: s.accountIDForSession(p), Session: &worldmap.DungeonSession{}, Room: &runtimeDungeonRoom{}}
			if kind == "town" {
				p.dungeon.runtime = nil
			}
			if kind == "wrong_map" {
				p.dungeon.runtime.Dungeon.ID = 999999
			}
			body := testQuestClearCouponRequestBody(4, 99)
			if kind == "reserved" {
				binary.LittleEndian.PutUint32(body[11:15], 1)
			}
			handled, err := s.handleAlignedGameCommand(p, 1, 515, body)
			if err != nil || !handled {
				t.Fatalf("处理=%v/%v", handled, err)
			}
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if packet.Header.MsgID != 515 || packet.Header.Classification != 1 {
				t.Fatalf("回包头=%+v", packet.Header)
			}
			after, _, _ := g.Inventory.Load(ctx, "29")
			if kind == "last_potion" || kind == "materials" {
				if !bytes.Equal(packet.Body, []byte{1, 4, 0, 0, 99, 0, 0, 0}) || len(rest) == 0 || len(after.Slots) != 0 {
					t.Fatalf("成功回包或扣数错误：%x %+v", packet.Body, after)
				}
				for len(rest) > 0 {
					next, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
					rest = tail
					if next.Header.MsgID != 14 {
						t.Fatalf("多余成功包=%d", next.Header.MsgID)
					}
				}
				if dir := os.Getenv("DNF90_RIDING_FIXTURES"); dir != "" {
					if err := os.MkdirAll(dir, 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "action99-ack.bin"), packet.Body, 0644); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				if !bytes.Equal(packet.Body, []byte{0, 1, 4, 0, 0, 99}) || len(rest) != 0 || !reflect.DeepEqual(bag, after) {
					t.Fatalf("拒绝仍扣数或回成功：%x %+v", packet.Body, after)
				}
			}
		})
	}
}
