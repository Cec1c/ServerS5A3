package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Exercise the real PVF, durable room receipt, system-mail writer, native op95
// claim route and vending owner together. No player or network state is used.
func TestRealBlackDiamond150FatigueMailClaimAndVending(t *testing.T) {
	f := newBlackDiamondShopFixture(t, 3000, 0, 1000)
	f.service.options.currentClientVersion = "official-24.9"
	f.seedPackage(t)
	f.openPackage(t)
	f.useVoucher(t)
	ctx := context.Background()
	now := time.Now().UTC()
	character, found, err := f.repositories.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("character: %t %v", found, err)
	}
	if character.Stats == nil {
		character.Stats = map[string]int64{}
	}
	character.Stats["fatigue"] = 39
	character.Stats["fatigue_limit"] = 188
	character.Stats[fatigue.DayKey] = premium.ServiceDayID(now)
	character.Stats[premium.BlackDiamondFatigueDayKey] = premium.ServiceDayID(now)
	character.Stats[premium.BlackDiamondFatigueSpentKey] = 149
	if err = f.repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	event := &dnfrepo.ActivityEvent{ID: "blackdiamond150:room", AccountID: blackDiamondShopAccountID, CharacterID: "19", Metric: dnfrepo.ActivityFatigue, Amount: 1, OccurredAt: now, DungeonID: 100, Source: "dungeon_room"}
	if _, err = f.service.updateCurrentCharacterFatigue(ctx, f.session, true, event); err != nil {
		t.Fatal(err)
	}
	character, _, err = f.repositories.Character.Load(ctx, "19")
	if err != nil || character.Stats["fatigue"] != 38 || character.Stats[premium.BlackDiamondFatigueCreditedTotalKey] != 1 || character.Stats[premium.BlackDiamondFatigueDeliveredTotalKey] != 1 {
		t.Fatalf("150-fatigue debit/mail ledger: stats=%v err=%v", character.Stats, err)
	}
	box, found, err := f.repositories.Mailbox.Load(ctx, "19")
	if err != nil || !found || len(box.Mails) != 1 {
		t.Fatalf("mailbox: %+v found=%t err=%v", box, found, err)
	}
	var mailID uint32
	for id, mail := range box.Mails {
		n, parseErr := strconv.ParseUint(id, 10, 32)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		mailID = uint32(n)
		if len(mail.Attachments) != 1 || mail.Attachments[0].ItemID != 7454 || mail.Attachments[0].Count != 1 || !mail.Attachments[0].ExpireAt.IsZero() {
			t.Fatalf("ordinary real PVF coin attachment: %+v", mail.Attachments)
		}
	}
	// Replay of the same real room receipt must not charge FP or earn mail again.
	if _, err = f.service.updateCurrentCharacterFatigue(ctx, f.session, true, event); err != nil {
		t.Fatal(err)
	}
	character, _, _ = f.repositories.Character.Load(ctx, "19")
	box, _, _ = f.repositories.Mailbox.Load(ctx, "19")
	if character.Stats["fatigue"] != 38 || character.Stats[premium.BlackDiamondFatigueSpentKey] != 150 || len(box.Mails) != 1 {
		t.Fatalf("replayed debit: %v mails=%d", character.Stats, len(box.Mails))
	}
	handled, err := p157RealMailClaim(t, f.service, f.session, mailID)
	if err != nil || !handled {
		t.Fatalf("real op95 claim: handled=%t err=%v", handled, err)
	}
	if success, code := p157RealMailAck(t, f.service, f.session); !success {
		t.Fatalf("coin claim refused: %x", code)
	}
	inventory := f.inventory(t)
	coinSlot := int16(-1)
	for key, stack := range inventory.Slots {
		if stack.ItemID == 7454 && stack.Count == 1 && len(key) > 2 && key[:2] == "0:" {
			n, e := strconv.Atoi(key[2:])
			if e != nil {
				t.Fatal(e)
			}
			coinSlot = int16(n)
		}
	}
	if coinSlot < 0 {
		t.Fatalf("claimed coin was not routed to real main inventory: %+v", inventory.Slots)
	}
	f.service.dungeonChoice = func(limit int) (int, error) { return 0, nil }
	body := make([]byte, 10)
	binary.LittleEndian.PutUint32(body, 1)
	binary.LittleEndian.PutUint32(body[4:], 1)
	binary.LittleEndian.PutUint16(body[8:], uint16(coinSlot))
	f.connection.write.Reset()
	if err = f.service.handleCurrentVendingMachine(f.session, body); err != nil {
		t.Fatal(err)
	}
	packets := splitAllGameServerUpperPackets(t, f.connection.write.Bytes())
	if len(packets) == 0 || packets[0].Header.MsgID != 215 || len(packets[0].Body) < 1 || packets[0].Body[0] != 1 {
		t.Fatalf("earned coin vending response: %+v", packets)
	}
	for _, stack := range f.inventory(t).Slots {
		if stack.ItemID == 7454 && stack.Count > 0 {
			t.Fatal("vending did not consume the single earned coin")
		}
	}
	if _, err = p157RealMailClaim(t, f.service, f.session, mailID); err != nil {
		t.Fatal(err)
	}
	for _, stack := range f.inventory(t).Slots {
		if stack.ItemID == 7454 && stack.Count > 0 {
			t.Fatal("repeated mail claim reissued the spent coin")
		}
	}
}
