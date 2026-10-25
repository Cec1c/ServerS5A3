package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/itemexpiration"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func merchantPurchaseSource(t *testing.T, source dungeonDropCatalogTestSource) {
	t.Helper()
	list := "[shop list]\n"
	for index, base := range []uint32{390, 389} {
		basePath := []string{"defence.npc", "mercenary.npc"}[index]
		upgrade := "\n[upgrade]\n`guild agit` "
		for level := 1; level <= 10; level++ {
			shop := 201 + index*10 + level - 1
			npc := 420 - index*10 + level
			list += fmt.Sprintf("%d ", shop)
			upgrade += fmt.Sprintf("%d %d ", level, npc)
			source["npc/npc.lst"] += fmt.Sprintf("\n%d `%d.npc`", npc, npc)
			source[fmt.Sprintf("npc/%d.npc", npc)] = fmt.Sprintf("[role]\n`[item shop]` %d\n[/role]", shop)
			source["itemshop/itemshop.lst"] += fmt.Sprintf("\n%d `%d.shp`", shop, shop)
			source[fmt.Sprintf("itemshop/%d.shp", shop)] = fmt.Sprintf("[NPC] %d\n[type] `[agitwar build shop]`\n[item list]\n10156091\n[/item list]", npc)
		}
		source["npc/"+basePath] += upgrade + "\n[/upgrade]"
		_ = base
	}
	source["etc/agitwar.etc"] += "\n" + list + "\n[/shop list]"
	source["stackable/stackable.lst"] = "10156091 `merchant.stk`"
	source["stackable/merchant.stk"] = "[name] `merchant fixture`\n[stackable type] `[expert town potion]` 0\n[minimum level] 17\n[stack limit] 1000\n[daily delete item] 1\n[require ap] 2"
}

func merchantPurchaseFixture(t *testing.T) (*Service, *gameSession, r.Group) {
	t.Helper()
	s, p, repos, source := guildMerchantFixture(t)
	merchantPurchaseSource(t, source)
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = items
	actor, _, _ := repos.Character.Load(context.Background(), "29")
	actor.Level = 90
	if err = repos.Character.Save(context.Background(), actor); err != nil {
		t.Fatal(err)
	}
	return s, p, repos
}

func merchantPurchaseBody(item, quantity, shop, npc uint32) []byte {
	body := make([]byte, 16)
	for i, n := range []uint32{item, quantity, shop, npc} {
		binary.LittleEndian.PutUint32(body[i*4:], n)
	}
	return body
}

func TestGuildMerchantPurchaseProductionAndReplay(t *testing.T) {
	for _, tc := range []struct {
		shop, npc uint32
		x         uint16
	}{{201, 421, 1005}, {211, 411, 900}} {
		t.Run(fmt.Sprint(tc.shop), func(t *testing.T) {
			s, p, repos := merchantPurchaseFixture(t)
			p.townPositionSnapshot.PositionX = tc.x
			for step, seq := range []uint16{60, 61, 60} {
				p.conn.(*bufferConn).write.Reset()
				sendMerchantRequest(t, s, p, 1354, merchantPurchaseBody(10156091, 3, tc.shop, tc.npc), seq)
				if got := guildReply(t, p, 1354); !bytes.Equal(got, []byte{1}) {
					t.Fatalf("ACK %x", got)
				}
				exportEssenceBody(t, fmt.Sprintf("merchant-buy-%d-step%d-1354", tc.shop, step), guildReply(t, p, 1354))
			}
			inv, _, _ := repos.Inventory.Load(context.Background(), "29")
			count := int64(0)
			for _, item := range inv.Slots {
				if item.ItemID == 10156091 {
					count += item.Count
					if !item.ExpireAt.Equal(itemexpiration.NextDailyDeleteAt(s.gameplayNow())) || len(item.RawEntry) == 0 {
						t.Fatal("item projection missing")
					}
				}
			}
			reg, err := guild.Load(context.Background(), repos.Settings)
			if err != nil || count != 6 || reg.Guilds[1].Essence.AP != 99988 || len(reg.MerchantPurchaseReceipts) != 2 {
				t.Fatalf("count=%d registry=%+v err=%v", count, reg, err)
			}
		})
	}
}

func TestGuildMerchantPurchaseProductionFailureACKAndRollback(t *testing.T) {
	for _, kind := range []string{"short", "item", "npc", "level", "quantity", "wrong-area", "not-ready", "balance", "full"} {
		t.Run(kind, func(t *testing.T) {
			s, p, repos := merchantPurchaseFixture(t)
			ctx := context.Background()
			body := merchantPurchaseBody(10156091, 3, 201, 421)
			switch kind {
			case "short":
				body = body[:15]
			case "item":
				binary.LittleEndian.PutUint32(body, 999)
			case "npc":
				binary.LittleEndian.PutUint32(body[12:], 390)
			case "level":
				binary.LittleEndian.PutUint32(body[8:], 202)
				binary.LittleEndian.PutUint32(body[12:], 422)
			case "quantity":
				binary.LittleEndian.PutUint32(body[4:], 1001)
			case "wrong-area":
				p.townPositionSnapshot.AreaID = 7
			case "not-ready":
				p.townSceneReadyCharacterID = 0
			case "balance":
				reg, _ := guild.Load(ctx, repos.Settings)
				reg.Guilds[1].Essence.AP = 5
				if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			case "full":
				inv, _, _ := repos.Inventory.Load(ctx, "29")
				for slot := 0; slot < 300; slot++ {
					inv.Slots[fmt.Sprintf("0:%d", slot)] = r.ItemStack{ItemID: 777, Count: 1}
				}
				if err := repos.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			beforeInv, _, _ := repos.Inventory.Load(ctx, "29")
			p.conn.(*bufferConn).write.Reset()
			sendMerchantRequest(t, s, p, 1354, body, 60)
			if got := guildReply(t, p, 1354); len(got) != 2 || got[0] != 0 {
				t.Fatalf("failure ACK %x", got)
			}
			exportEssenceBody(t, "merchant-buy-reject-"+kind+"-1354", guildReply(t, p, 1354))
			after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			afterInv, _, _ := repos.Inventory.Load(ctx, "29")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeInv, afterInv) {
				t.Fatal("failed purchase changed assets")
			}
		})
	}
}

type merchantExtractedSource map[string]string

func (s merchantExtractedSource) ReadText(path string) (string, error) {
	text, ok := s[strings.ToLower(cleanDungeonDropPath(path))]
	if !ok {
		return "", errors.New("missing merchant extracted file: " + path)
	}
	return text, nil
}

func TestGuildMerchantRealExtractedCatalogAndPurchases(t *testing.T) {
	root := os.Getenv("GUILD_MERCHANT_EXTRACTED_ROOT")
	if root == "" {
		t.Skip("set GUILD_MERCHANT_EXTRACTED_ROOT")
	}
	s, p, repos, source := guildMerchantFixture(t)
	for _, sub := range []string{"pvf", "pvf-meta", "pvf-maps"} {
		base := filepath.Join(root, sub)
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(base, path)
			if err != nil {
				return err
			}
			source[cleanDungeonDropPath(rel)] = strings.TrimPrefix(string(raw), "\ufeff")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	items, err := newPVFDungeonDropCatalog(merchantExtractedSource(source))
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = items
	catalog, err := loadGuildMerchantPurchaseCatalog(items)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Shops) != 20 {
		t.Fatal(len(catalog.Shops))
	}
	ctx := context.Background()
	actor, _, _ := repos.Character.Load(ctx, "29")
	actor.Level = 90
	if err = repos.Character.Save(ctx, actor); err != nil {
		t.Fatal(err)
	}
	seen := map[uint32]bool{}
	seq := uint16(100)
	for id := uint32(201); id <= 220; id++ {
		shop := catalog.Shops[id]
		p.townPositionSnapshot.PositionX = 1005
		if shop.Index == 1 {
			p.townPositionSnapshot.PositionX = 900
		}
		reg, _ := guild.Load(ctx, repos.Settings)
		reg.Guilds[1].Merchants = &guild.MerchantState{Levels: [2]uint8{shop.Level, shop.Level}}
		reg.Guilds[1].Essence.AP = 1000000
		if err = guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
			t.Fatal(err)
		}
		for itemID, product := range shop.Products {
			seen[itemID] = true
			seq++
			if err = repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "29", Slots: map[string]r.ItemStack{}}); err != nil {
				t.Fatal(err)
			}
			before, _ := guild.Load(ctx, repos.Settings)
			p.conn.(*bufferConn).write.Reset()
			sendMerchantRequest(t, s, p, 1354, merchantPurchaseBody(itemID, 2, id, shop.NPCID), seq)
			if got := guildReply(t, p, 1354); !bytes.Equal(got, []byte{1}) {
				t.Fatalf("shop=%d item=%d ACK %x", id, itemID, got)
			}
			after, _ := guild.Load(ctx, repos.Settings)
			if before.Guilds[1].Essence.AP-after.Guilds[1].Essence.AP != product.AP*2 {
				t.Fatal("AP cost")
			}
			inv, _, _ := repos.Inventory.Load(ctx, "29")
			if len(inv.Slots) != 1 {
				t.Fatal("grant slots")
			}
			for _, stack := range inv.Slots {
				if stack.ItemID != int64(itemID) || stack.Count != 2 || !stack.ExpireAt.Equal(itemexpiration.NextDailyDeleteAt(s.gameplayNow())) || len(stack.RawEntry) == 0 {
					t.Fatalf("grant %+v", stack)
				}
			}
		}
	}
	if len(seen) != 132 {
		t.Fatalf("unique products %d", len(seen))
	}
	t.Logf("20 real shops, %d unique products, %d purchases, daily deadline %s", len(seen), seq-100, itemexpiration.NextDailyDeleteAt(s.gameplayNow()).Format(time.RFC3339))
}
