package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/experiencebuff"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func privateExperience002Archive(t *testing.T) *platformpvf.Archive {
	t.Helper()
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("requires paired PVF")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestPrivateExperience002PurchasedQuestPotionNativeRoute(t *testing.T) {
	a := privateExperience002Archive(t)
	for _, level := range []int{1, 45, 89, 90} {
		t.Run(strconv.Itoa(level), func(t *testing.T) {
			s, session, g, q, category, product := newAdventureShopExpirationFixture(t, a)
			s.initialEquipmentArchive = a
			s.bindGameSessionCharacter(session, 31)
			ctx := context.Background()
			// Purchase while eligible for the adventure shop, then exercise the
			// account-bound potion at the recipient's level independently.
			buyAdventureExpirationPotion(t, s, session, category.Index, product.ItemID, true)
			c, _, _ := g.Character.Load(ctx, "31")
			c.Level = level
			g.Character.Save(ctx, c)
			bag, _, _ := g.Inventory.Load(ctx, "31")
			var slot int16
			for key := range bag.Slots {
				var list int
				fmt.Sscanf(key, "%d:%d", &list, &slot)
			}
			conn := session.conn.(*bufferConn)
			conn.write.Reset()
			body := testQuestClearCouponRequestBody(slot, 194)
			if err := s.handleGameCommand(session, 1, 515, body); err != nil {
				t.Fatal(err)
			}
			after, _, _ := g.Inventory.Load(ctx, "31")
			c, _, _ = g.Character.Load(ctx, "31")
			if os.Getenv("EXPERIENCE_EXPECT_OLD") == "1" {
				if !reflect.DeepEqual(after.Slots, bag.Slots) || experiencebuff.Bonus(c, experiencebuff.Quest, 1000, q.now) != 0 {
					t.Fatal("old route unexpectedly works")
				}
				return
			}
			if len(after.Slots) != 0 || experiencebuff.Bonus(c, experiencebuff.Quest, 1000, q.now) != 1000 {
				t.Fatalf("not applied: bag=%v stats=%v", after.Slots, c.Stats)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != 515 || len(ack.Body) != 8 || ack.Body[0] != 1 || binary.LittleEndian.Uint32(ack.Body[4:]) != 194 {
				t.Fatalf("native ACK=%v %x", ack.Header, ack.Body)
			}
			refresh, _ := splitGameServerUpperPacket(t, rest)
			if refresh.Header.MsgID != 14 {
				t.Fatal("no authoritative slot refresh")
			}
			before := repo.CloneCharacter(c)
			conn.write.Reset()
			if err := s.handleGameCommand(session, 1, 515, body); err != nil {
				t.Fatal(err)
			}
			c, _, _ = g.Character.Load(ctx, "31")
			if !reflect.DeepEqual(c.Stats, before.Stats) {
				t.Fatal("replay changed stats")
			}
			ack, _ = splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Body[0] != 0 {
				t.Fatal("replay accepted")
			}
			if experiencebuff.Bonus(c, experiencebuff.Quest, 1000, q.now.Add(30*time.Minute)) != 0 {
				t.Fatal("bonus outlives duration")
			}
		})
	}
}

func TestPrivateExperience002NativeRouteRejectsMalformedAndForeign(t *testing.T) {
	a := privateExperience002Archive(t)
	for _, name := range []string{"wrong_item", "foreign_owner", "bad_list", "reserved", "truncated", "expired", "cooldown"} {
		t.Run(name, func(t *testing.T) {
			s, session, g, q, category, product := newAdventureShopExpirationFixture(t, a)
			s.initialEquipmentArchive = a
			s.bindGameSessionCharacter(session, 31)
			buyAdventureExpirationPotion(t, s, session, category.Index, product.ItemID, true)
			ctx := context.Background()
			bag, _, _ := g.Inventory.Load(ctx, "31")
			var slot int16
			var key string
			for k := range bag.Slots {
				key = k
				var list int
				fmt.Sscanf(k, "%d:%d", &list, &slot)
			}
			body := testQuestClearCouponRequestBody(slot, 194)
			switch name {
			case "wrong_item":
				x := bag.Slots[key]
				x.ItemID = 7440
				bag.Slots[key] = x
				g.Inventory.Save(ctx, bag)
			case "foreign_owner":
				c, _, _ := g.Character.Load(ctx, "31")
				c.AccountID = "another-account"
				g.Character.Save(ctx, c)
			case "bad_list":
				body[2] = 1
			case "reserved":
				body[3] = 1
			case "truncated":
				body = body[:15]
			case "expired":
				x := bag.Slots[key]
				x.ExpireAt = q.now
				bag.Slots[key] = x
				g.Inventory.Save(ctx, bag)
			case "cooldown":
				c, _, _ := g.Character.Load(ctx, "31")
				c.Stats = map[string]int64{"experience_potion_quest_cooldown_until_ms": q.now.Add(time.Hour).UnixMilli()}
				g.Character.Save(ctx, c)
			}
			before, _, _ := g.Inventory.Load(ctx, "31")
			cb, _, _ := g.Character.Load(ctx, "31")
			conn := session.conn.(*bufferConn)
			conn.write.Reset()
			if err := s.handleGameCommand(session, 1, 515, body); err != nil {
				t.Fatal(err)
			}
			after, _, _ := g.Inventory.Load(ctx, "31")
			ca, _, _ := g.Character.Load(ctx, "31")
			if !reflect.DeepEqual(before.Slots, after.Slots) || !reflect.DeepEqual(cb.Stats, ca.Stats) {
				t.Fatal("rejected use mutated assets")
			}
			ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != 515 || ack.Body[0] != 0 {
				t.Fatal("failure ACK missing")
			}
		})
	}
}
