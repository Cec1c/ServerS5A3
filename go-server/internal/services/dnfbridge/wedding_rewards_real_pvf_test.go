package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"sort"
	"strconv"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/wedding"
	archive "longheng.io/server/internal/platform/pvf"
)

func TestWeddingGiftPVFDefinitions(t *testing.T) {
	path := os.Getenv("DNF_WEDDING_TEST_PVF")
	if path == "" {
		t.Skip("requires paired runtime PVF")
	}
	source, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := wedding.Load(source)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	shop, err := parseDungeonCardPVFDocument(source, "etc/cerashop.etc")
	if err != nil {
		t.Fatal(err)
	}
	discounts, _ := shop.Section("discount list")
	t.Logf("coupon-catalog discount-list tokens=%d", len(discounts))
	for kind := byte(0); kind < 3; kind++ {
		gift, ok := rules.CeremonyGifts[kind]
		if !ok {
			t.Fatalf("missing gift kind %d", kind)
		}
		item, err := catalog.ResolveItem(uint32(gift.ItemID))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("kind=%d gift=%d count=%d path=%s", kind, gift.ItemID, gift.Count, item.PVFPath)
		for job := 0; job < 16; job++ {
			c := r.CharacterRecord{CharacterID: "19", AccountID: "account-1", Job: strconv.Itoa(job), Level: 90}
			definition, err := resolveCurrentBoosterDefinitionForCharacter(catalog, uint32(gift.ItemID), currentBoosterRequestRandom, &c)
			if err != nil {
				t.Fatalf("kind=%d job=%d: %v", kind, job, err)
			}
			amounts, options, err := resolveCurrentBoosterRewardAmounts(definition, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom}, func(int64) (int64, error) { return 0, nil })
			if err != nil {
				t.Fatal(err)
			}
			if amounts[10002901] != []uint32{10, 30, 100}[kind] {
				t.Fatalf("kind=%d job=%d wrong point reward %v", kind, job, amounts)
			}
			if kind > 0 && amounts[10002611] != 1 {
				t.Fatal("missing low ring ticket", amounts)
			}
			if kind == 2 && (amounts[10002612] != 1 || len(amounts) != 5) {
				t.Fatal("luxury gift leaked other jobs or lost rewards", amounts)
			}
			t.Logf("kind=%d job=%d rewards=%v options=%v", kind, job, amounts, options)
			g := memory.NewMemoryGroup()
			ctx := context.Background()
			if err := g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			if err := g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "19", Slots: map[string]r.ItemStack{"0:75": {ItemID: gift.ItemID, Count: 1}}}); err != nil {
				t.Fatal(err)
			}
			s := currentBoosterTestService(catalog, g)
			p := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
			if err := s.handleCurrentBoosterItem(p, []byte{75, 0}); err != nil {
				t.Fatal(err)
			}
			if ack := guildReply(t, p, 160); len(ack) == 0 || ack[0] != 1 {
				t.Fatalf("kind=%d job=%d ACK=%x", kind, job, ack)
			}
			bag, _, err := g.Inventory.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			actual := map[uint32]uint32{}
			for _, stack := range bag.Slots {
				if stack.ItemID == gift.ItemID {
					t.Fatal("box not consumed")
				}
				actual[uint32(stack.ItemID)] += uint32(stack.Count)
			}
			for id, count := range amounts {
				if actual[id] != count {
					t.Fatalf("kind=%d job=%d item=%d count=%d want=%d", kind, job, id, actual[id], count)
				}
			}
			ids := make([]int, 0, len(amounts))
			for id := range amounts {
				ids = append(ids, int(id))
			}
			sort.Ints(ids)
			for _, id := range ids {
				child, err := catalog.ResolveItem(uint32(id))
				if err != nil {
					t.Fatal(err)
				}
				if child.Kind != dungeonDropItemStackable {
					continue
				}
				doc, err := parseDungeonCardPVFDocument(source, child.PVFPath)
				if err != nil {
					t.Fatal(err)
				}
				typ, _ := doc.Text("stackable type")
				if job == 0 || kind == 2 {
					t.Logf("nested job=%d item=%d type=%s path=%s", job, id, typ, child.PVFPath)
				}
				if typ == "[coupon]" && job == 0 {
					text, err := source.ReadText(child.PVFPath)
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("coupon-definition item=%d\n%s", id, text)
				}
				if typ == "[usable cera package]" {
					pack, err := resolveCurrentCeraPackageDefinition(catalog, uint32(id))
					if err != nil {
						t.Fatal(err)
					}
					var slot int16 = -1
					for key, stack := range bag.Slots {
						list, index, ok := parseSceneInventorySlotKey(key)
						if ok && list == 0 && stack.ItemID == int64(id) {
							slot = index
						}
					}
					if slot < 0 {
						t.Fatal("nested package was not actually delivered", id)
					}
					body := make([]byte, 3+5*len(pack.AvatarItemIDs))
					binary.LittleEndian.PutUint16(body, uint16(slot))
					body[2] = byte(len(pack.AvatarItemIDs))
					for i, avatar := range pack.AvatarItemIDs {
						binary.LittleEndian.PutUint32(body[3+i*5:], avatar)
					}
					p.conn.(*bufferConn).write.Reset()
					if err := s.handleCurrentCeraPackageOpen(p, body); err != nil {
						t.Fatal(err)
					}
					if ack := guildReply(t, p, 518); len(ack) != 3 || ack[0] != 1 {
						t.Fatalf("job=%d package=%d ACK=%x", job, id, ack)
					}
					opened, _, err := g.Inventory.Load(ctx, "19")
					if err != nil {
						t.Fatal(err)
					}
					counts := map[uint32]uint32{}
					for _, stack := range opened.Slots {
						counts[uint32(stack.ItemID)] += uint32(stack.Count)
					}
					for _, reward := range pack.Rewards {
						if counts[reward.Definition.ItemID] != reward.Count {
							t.Fatalf("job=%d nested=%d item=%d got=%d want=%d", job, id, reward.Definition.ItemID, counts[reward.Definition.ItemID], reward.Count)
						}
					}
					if counts[uint32(id)] != 0 {
						t.Fatal("nested package not consumed", id)
					}
					t.Logf("nested-package job=%d item=%d avatars=%d native518=passed", job, id, len(pack.AvatarItemIDs))
					bag = opened
				} else if typ == "[booster]" {
					nested, err := resolveCurrentBoosterDefinitionForCharacter(catalog, uint32(id), currentBoosterRequestRandom, &c)
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("nested-booster job=%d item=%d groups=%+v", job, id, nested.Random.Groups)
				}
			}
		}
	}
}
