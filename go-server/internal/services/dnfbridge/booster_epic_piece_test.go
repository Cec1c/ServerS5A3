package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func epicGiftFixture(t *testing.T, catalog *pvfDungeonDropCatalog, box uint32) (*Service, *gameSession, *bufferConn, repo.Group) {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	for _, err := range []error{g.Account.Save(ctx, repo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{repo.AccountEpicPieceMetadataKey(10300783): "7", "other": "keep"}}), g.Character.Save(ctx, repo.CharacterRecord{AccountID: "account-1", CharacterID: "19", Stats: map[string]int64{"gold": 999999}}), g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"0:75": {ItemID: int64(box), Count: 1, ExpireAt: time.Now().Add(24 * time.Hour)}}})} {
		if err != nil {
			t.Fatal(err)
		}
	}
	s := currentBoosterTestService(catalog, g)
	s.options.epicPiecePush = readEpicPiecePushMode("")
	s.dungeonMonsterTable = &pvfDungeonMonsterCatalog{source: catalog.source}
	conn := &bufferConn{}
	return s, &gameSession{conn: conn, connID: "礼包碎片实时更新", selectedCharacterID: 19}, conn, g
}

func assertEpicGiftOutcome(t *testing.T, s *Service, session *gameSession, conn *bufferConn, g repo.Group, body []byte, count uint32) {
	t.Helper()
	ctx := context.Background()
	for attempt := 0; attempt < 2; attempt++ {
		conn.write.Reset()
		if err := s.handleCurrentBoosterItem(session, body); err != nil {
			t.Fatal(err)
		}
		data := conn.write.Bytes()
		updates := 0
		sawAck := false
		for len(data) > 0 {
			p, rest := splitGameServerUpperPacket(t, data)
			data = rest
			if p.Header.MsgID == 160 {
				sawAck = true
				if attempt == 0 && (len(p.Body) == 0 || p.Body[0] != 1) {
					req, e := parseCurrentBoosterOpenRequest(body)
					if e != nil {
						t.Fatal(e)
					}
					catalog, e := s.currentPVFItemCatalog()
					if e != nil {
						t.Fatal(e)
					}
					definition, e := s.prepareCurrentBooster(ctx, session, catalog, req)
					if e != nil {
						t.Fatalf("礼包预检失败：%v", e)
					}
					_, e = s.commitCurrentBooster(ctx, session, catalog, definition, req)
					t.Fatalf("开盒失败：%x；事务错误：%v", p.Body, e)
				}
			}
			if p.Header.MsgID == 1149 {
				updates++
				if !sawAck || len(p.Body) != 13 || p.Body[0] != 1 || binary.LittleEndian.Uint32(p.Body[5:]) != 10300783 || binary.LittleEndian.Uint32(p.Body[9:]) != 7+count {
					t.Fatalf("礼包没有立即同步正确碎片余额：%x", p.Body)
				}
			}
		}
		if (attempt == 0 && updates != 1) || (attempt == 1 && updates != 0) {
			t.Fatal("开盒刷新次数或重复请求不正确", attempt, updates)
		}
		a, _, _ := g.Account.Load(ctx, "account-1")
		if repo.AccountEpicPieceBalances(a)[10300783] != 7+count || a.Metadata["other"] != "keep" {
			t.Fatal("礼包入账或重复开盒错误", a.Metadata)
		}
		inv, _, _ := g.Inventory.Load(ctx, "19")
		for _, row := range inv.Slots {
			if row.ItemID == 10300783 {
				t.Fatal("礼包碎片进入普通背包")
			}
		}
		if _, ok := inv.Slots["0:75"]; ok {
			t.Fatal("礼包没有消耗")
		}
	}
}

func TestEpicPieceLiveRandomGiftCreditsBookWithoutInventorySlot(t *testing.T) {
	source := dungeonDropCatalogTestSource{"equipment/equipment.lst": "", "stackable/stackable.lst": "900 `gift.stk`\n10300783 `epicPiece/10300783.stk`\n", currentEpicPieceInfoPath: "[piece list]\n100312425 10300783\n[/piece list]\n", "stackable/gift.stk": "[stackable type]\n`[booster]` 0\n[stack limit]\n999\n[booster info]\n[etc]\n1 10300783 10000 30\n[/etc]\n[/booster info]\n", "stackable/epicPiece/10300783.stk": "[stackable type]\n`[material]` 0\n[stack limit]\n999\n"}
	source["monster/monster.lst"] = ""
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s, session, conn, g := epicGiftFixture(t, catalog, 900)
	assertEpicGiftOutcome(t, s, session, conn, g, []byte{75, 0}, 30)
}

func TestRealPVFEpicPieceLiveSelectableGiftBoxes(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("设置真实PVF验证两种碎片自选礼包")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct{ id, count uint32 }{{10159817, 30}, {10164880, 50}} {
		t.Run(fmt.Sprint(sample.id), func(t *testing.T) {
			definition, err := resolveCurrentBoosterDefinition(catalog, sample.id, currentBoosterRequestSelection)
			if err != nil {
				t.Fatal(err)
			}
			var category uint16
			found := false
			for id, rows := range definition.SelectionCategory {
				for _, row := range rows {
					if row.ItemID == 10300783 {
						if row.Count != sample.count {
							t.Fatal("PVF碎片数量与现场记录不一致", row)
						}
						category = id
						found = true
					}
				}
			}
			if !found {
				t.Fatal("真实礼包未找到目标碎片")
			}
			s, session, conn, g := epicGiftFixture(t, catalog, sample.id)
			body := make([]byte, 10)
			binary.LittleEndian.PutUint16(body, 75)
			binary.LittleEndian.PutUint16(body[2:], category)
			binary.LittleEndian.PutUint32(body[4:], 10300783)
			assertEpicGiftOutcome(t, s, session, conn, g, body, sample.count)
		})
	}
}

func TestRealPVFEpicPieceGiftEntriesUseSupportedBoosterRoute(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("设置真实PVF核对礼包碎片发放入口")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	book, err := loadCurrentEpicPieceCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	ids := regexp.MustCompile(`\b103[0-9]{5}\b`)
	found := 0
	for _, itemID := range catalog.stackableIDs {
		ref := catalog.itemRefs[itemID]
		fullPath, text, err := readDungeonDropText(archive, "stackable", ref.path)
		if err != nil {
			continue
		}
		// 仅审查有奖励表的盒子，碎片配方中的自身编号不算礼包产出。
		if !strings.Contains(text, "[booster") && !strings.Contains(text, "[int data]") && !strings.Contains(text, "[package]") {
			continue
		}
		hasPiece := false
		for _, word := range ids.FindAllString(text, -1) {
			id, _ := strconv.ParseUint(word, 10, 32)
			if book.isEpicPiece(uint32(id)) {
				hasPiece = true
				break
			}
		}
		if !hasPiece {
			continue
		}
		doc, err := parseDungeonCardPVFDocument(archive, fullPath)
		if err != nil {
			t.Fatal(err)
		}
		kind := normalizeDungeonDropStackableType(magicBoxDocumentText(doc, "stackable type"))
		if kind != "[booster selection]" && kind != "[booster]" && kind != "[booster random]" && kind != "[cera booster]" {
			t.Fatalf("碎片礼包还有独立入口待接入：物品%d 类型%s 路径%s", itemID, kind, ref.path)
		}
		found++
		t.Logf("碎片奖励入口：物品%d 类型%s 路径%s", itemID, kind, ref.path)
	}
	if found < 2 {
		t.Fatal("未覆盖现用碎片礼包", found)
	}
	t.Logf("已扫描%d份堆叠物品索引，发现%d种含真实碎片编号的奖励表入口", len(catalog.stackableIDs), found)
}
