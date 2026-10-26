package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestP144FlagUpgradeRealPolicy(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
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
	s := currentNPCShopTestService(mustCurrentNPCShopRepositories(t, 0, nil), shop, items)
	s.initialEquipmentArchive = archive
	resolve := s.currentGuildMedalUpgradeResolver()
	// Native fild/fmul(float32)/fistp(truncate), not decimal rounding.
	counts := []int64{18, 36, 55, 73, 91, 110, 128, 147, 165, 183}
	rates := []int{100000, 85000, 72000, 61000, 52000, 44000, 38000, 32000, 27000, 23000}
	penalties := []int{1, 1, 1, 1, 2, 2, 2, 2, 2, 2}
	values := []int{0, 0, 0, 0, 1, 1, 1, 3, 3, 3}
	for level := byte(0); level < 20; level++ {
		t.Run(fmt.Sprintf("level%d", level), func(t *testing.T) {
			p, err := resolve(100380051, level)
			if level >= 10 {
				if err == nil {
					t.Fatal("unverified destroy/payback enabled")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.MaterialID != 10154439 || p.MaterialCount != counts[level] || p.SuccessWeight != rates[level] || p.PenaltyType != penalties[level] || p.PenaltyValue != values[level] {
				t.Fatalf("policy=%+v", p)
			}
		})
	}
	for _, id := range []int64{28309, 90002, 10154439, 0, -1} {
		if _, err := resolve(id, 0); err == nil {
			t.Fatalf("non-medal %d accepted", id)
		}
	}
}

// Exact writer order at v150 0x25fe47c..573; a replay fixture, not a capture.
func p144FlagUpgradeBody() []byte {
	b := []byte{0, 38, 0, 0}
	b = binary.LittleEndian.AppendUint32(b, 100380051)
	b = binary.LittleEndian.AppendUint16(b, 121)
	b = binary.LittleEndian.AppendUint16(b, 0xffff)
	b = binary.LittleEndian.AppendUint32(b, 4)
	b = append(b, []byte("test")...)
	b = binary.LittleEndian.AppendUint16(b, 0xffff)
	return binary.LittleEndian.AppendUint16(b, 0xffff)
}

func TestP144FlagUpgradeNativeHandler(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
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
	def, err := items.ResolveItem(100380051)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"success", "missing_material", "wrong_material", "wrong_template", "gem_page", "locked", "wrong_account", "amplify", "ticket", "npc_trailer", "truncated", "main_only", "unsupported_destroy_level", "no_gold", "expired_medal", "expired_material", "raw_identity"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			inv := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{}}
			_, err := grantCurrentGuildMedalPageItem(&inv, def, currentPVFItemTargetContainer(def), 1)
			if err != nil {
				t.Fatal(err)
			}
			medal := inv.Slots["38:0"]
			medal.RawEntry[0x57] = 7
			medal.Extra["p144_identity"] = "retained"
			inv.Slots["38:0"] = medal
			inv.Slots["0:121"] = dnfrepo.ItemStack{ItemID: 10154439, Count: 18}
			inv.Slots["0:122"] = dnfrepo.ItemStack{ItemID: 10154439, Count: 999}
			inv.Slots["0:9"] = dnfrepo.ItemStack{ItemID: 28309, Count: 1}
			body := p144FlagUpgradeBody()
			switch name {
			case "missing_material":
				v := inv.Slots["0:121"]
				v.Count = 17
				inv.Slots["0:121"] = v
			case "wrong_material":
				v := inv.Slots["0:121"]
				v.ItemID = 3037
				inv.Slots["0:121"] = v
			case "wrong_template":
				binary.LittleEndian.PutUint32(body[4:8], 28309)
			case "gem_page":
				binary.LittleEndian.PutUint16(body[2:4], 49)
			case "locked":
				medal.Extra["equipment_lock_state"] = "active"
				inv.Slots["38:0"] = medal
			case "expired_medal":
				medal.ExpireAt = time.Now().Add(-time.Hour)
				inv.Slots["38:0"] = medal
			case "expired_material":
				v := inv.Slots["0:121"]
				v.ExpireAt = time.Now().Add(-time.Hour)
				inv.Slots["0:121"] = v
			case "raw_identity":
				binary.LittleEndian.PutUint32(medal.RawEntry[2:6], 28309)
				inv.Slots["38:0"] = medal
			case "amplify":
				body[0] = 1
			case "ticket":
				binary.LittleEndian.PutUint16(body[10:12], 122)
			case "npc_trailer":
				binary.LittleEndian.PutUint16(body[len(body)-4:], 383)
			case "truncated":
				body = body[:len(body)-1]
			case "main_only":
				delete(inv.Slots, "38:0")
				inv.Slots["0:9"] = medal
			case "unsupported_destroy_level":
				medal.RawEntry[10] = 10
				medal.Extra["upgrade"] = "10"
				inv.Slots["38:0"] = medal
			}
			repos := mustCurrentNPCShopRepositories(t, 1000, inv.Slots)
			if name == "no_gold" {
				c, _, _ := repos.Character.Load(ctx, "19")
				c.Stats["gold"] = 0
				if err := repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			if name == "wrong_account" {
				c, _, _ := repos.Character.Load(ctx, "19")
				c.AccountID = "another"
				if err := repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := repos.Inventory.Load(ctx, "19")
			beforeChar, _, _ := repos.Character.Load(ctx, "19")
			s := currentNPCShopTestService(repos, shop, items)
			s.initialEquipmentArchive = archive
			conn := &bufferConn{}
			session := &gameSession{conn: conn, connID: "p144-flag", selectedCharacterID: 19}
			if err := s.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketUpgradeItem), body); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			after, _, _ := repos.Inventory.Load(ctx, "19")
			afterChar, _, _ := repos.Character.Load(ctx, "19")
			if name != "success" && name != "no_gold" {
				if ack.Body[0] != 0 || len(rest) != 0 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeChar, afterChar) {
					t.Fatalf("rejected request mutated state or succeeded: ack=%x", ack.Body)
				}
				return
			}
			if ack.Header.Classification != 1 || ack.Header.MsgID != 80 || len(ack.Body) != 25 || ack.Body[0] != 1 || ack.Body[14] != 38 || ack.Body[11] != 0 || ack.Body[13] != 1 || binary.LittleEndian.Uint16(ack.Body[15:17]) != 0 {
				t.Fatalf("native flag ACK mismatch: %x", ack.Body)
			}
			got := after.Slots["38:0"]
			if got.ItemID != medal.ItemID || got.Count != 1 || got.RawEntry[10]&31 != 1 || got.RawEntry[0x57] != 7 || got.Extra["p144_identity"] != "retained" || binary.LittleEndian.Uint16(got.RawEntry[11:13]) != 0 {
				t.Fatalf("medal row identity/level/durability changed incorrectly: %+v", got)
			}
			if _, ok := after.Slots["0:121"]; ok {
				t.Fatal("exact 18 certificate cost was not consumed")
			}
			if !reflect.DeepEqual(after.Slots["0:122"], before.Slots["0:122"]) || !reflect.DeepEqual(after.Slots["0:9"], before.Slots["0:9"]) {
				t.Fatal("unrelated rows changed")
			}
			if !reflect.DeepEqual(beforeChar, afterChar) {
				t.Fatalf("dedicated guild path changed character/wallet: before=%+v after=%+v", beforeChar, afterChar)
			}
			lists := map[byte]bool{}
			for len(rest) > 0 {
				p, tail := splitGameServerUpperPacket(t, rest)
				rest = tail
				if p.Header.MsgID == 14 {
					lists[p.Body[0]] = true
				}
			}
			if !lists[0] || !lists[38] {
				t.Fatalf("missing split updates: %v", lists)
			}
			fresh := currentNPCShopTestService(repos, shop, items)
			fresh.initialEquipmentArchive = archive
			projection, _, _, ok := fresh.buildCurrentItemListBodyForSession(ctx, session, 38)
			if !ok || len(projection) < 16 || projection[13]&31 != 1 {
				t.Fatalf("fresh medal projection lost level: %x", projection)
			}
		})
	}
}
