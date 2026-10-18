package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"
)

func TestQuestCouponNativeActionMustMatchPVF(t *testing.T) {
	for _, tc := range []struct {
		name   string
		item   int64
		action uint32
	}{{"gm_as_level", 10164630, 179}, {"level_as_gm", 10301410, 169}, {"historical_action", 10164630, 167}, {"guild_hongbao", 10164630, 162}} {
		t.Run(tc.name, func(t *testing.T) {
			s, session, repos := newQuestClearCouponTestService(t, 1)
			ctx := context.Background()
			inv, _, _ := repos.Inventory.Load(ctx, "17")
			stack := inv.Slots["0:87"]
			stack.ItemID = tc.item
			inv.Slots["0:87"] = stack
			if err := repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			quests, _, _ := repos.Quest.Load(ctx, "17")
			if _, err := s.handleCurrentQuestClearCouponUse(session, testQuestClearCouponRequestBody(87, tc.action)); err != nil {
				t.Fatal(err)
			}
			after, _, _ := repos.Inventory.Load(ctx, "17")
			afterQuests, _, _ := repos.Quest.Load(ctx, "17")
			if !reflect.DeepEqual(inv, after) || !reflect.DeepEqual(quests, afterQuests) {
				t.Fatal("foreign action changed inventory or quests")
			}
		})
	}
}

func TestQuestCouponMalformedEnvelopeDoesNotMutate(t *testing.T) {
	for _, offset := range []int{2, 3, 11, 15} {
		s, session, repos := newQuestClearCouponTestService(t, 1)
		ctx := context.Background()
		before, _, _ := repos.Inventory.Load(ctx, "17")
		quests, _, _ := repos.Quest.Load(ctx, "17")
		body := testQuestClearCouponRequestBody(87, 169)
		body[offset] = 1
		s.handleCurrentQuestClearCouponUse(session, body)
		after, _, _ := repos.Inventory.Load(ctx, "17")
		afterQuests, _, _ := repos.Quest.Load(ctx, "17")
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(quests, afterQuests) {
			t.Fatalf("offset %d bypassed request checks", offset)
		}
	}
}

func TestFatiguePotionMalformedActionReturnsCompleteFailure(t *testing.T) {
	for _, kind := range []string{"list", "slot", "reserved0", "reserved1", "reserved2", "truncated", "extended"} {
		t.Run(kind, func(t *testing.T) {
			s, session, repos := guildMileageFixture(t)
			ctx := context.Background()
			before, _, _ := repos.Inventory.Load(ctx, "29")
			body := testQuestClearCouponRequestBody(87, 58)
			switch kind {
			case "list":
				body[2] = 2
			case "slot":
				binary.LittleEndian.PutUint16(body, 65535)
			case "reserved0":
				body[3] = 1
			case "reserved1":
				body[11] = 1
			case "reserved2":
				body[15] = 1
			case "truncated":
				body = body[:11]
			case "extended":
				body = append(body, 0)
			}
			handled, err := s.handleCurrentFatiguePotionUse(session, body)
			if !handled || err != nil {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, session.conn.(*bufferConn).write.Bytes())
			if packet.Header.MsgID != 515 || packet.Header.Classification != 1 || !bytes.Equal(packet.Body, []byte{0, 1, body[0], body[1], body[2], 58}) || len(rest) != 0 {
				t.Fatalf("failure=%x rest=%x", packet.Body, rest)
			}
			after, _, _ := repos.Inventory.Load(ctx, "29")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("invalid potion action changed inventory")
			}
		})
	}
}
