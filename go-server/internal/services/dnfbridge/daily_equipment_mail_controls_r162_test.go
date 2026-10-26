package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestDailyEquipmentRealMailSourceAndOldRawControls(t *testing.T) {
	f, q, _, _ := dailyEquipmentFixture(t)
	defer f.service.stopAvatarAppearanceCoalesce(f.session, "mail-source-controls")
	for _, mode := range []string{"missing_source", "missing_marker", "unknown_old_source", "old_raw_zero", "old_hex_zero", "wrong_raw_id", "wrong_hex_id", "raw_deadline", "gm_conflict"} {
		t.Run(mode, func(t *testing.T) {
			ctx := resetDailyEquipmentMailInventory(t, f)
			if err := f.group.Mailbox.Save(ctx, dnfrepo.MailboxRecord{CharacterID: "19"}); err != nil {
				t.Fatal(err)
			}
			a := dnfrepo.MailAttachment{ItemID: 400300075, Count: 1, Extra: map[string]string{"retained": "yes"}}
			want := time.Time{}
			wantOK := mode == "old_raw_zero" || mode == "old_hex_zero" || mode == "raw_deadline" || mode == "gm_conflict"
			if mode == "old_raw_zero" || mode == "old_hex_zero" || mode == "wrong_raw_id" || mode == "wrong_hex_id" || mode == "raw_deadline" {
				a.RawEntry = loginWearAnchorRawEntry(9, a.ItemID, 12345, 0)
				if mode == "wrong_raw_id" || mode == "wrong_hex_id" {
					binary.LittleEndian.PutUint32(a.RawEntry[2:], 400300076)
				}
				if mode == "raw_deadline" {
					want = q.now.Add(2 * time.Hour)
					binary.LittleEndian.PutUint32(a.RawEntry[currentItemListExpireTimeOffset:], uint32(want.Unix()))
				}
				if mode == "old_hex_zero" || mode == "wrong_hex_id" {
					a.Extra["raw_entry_hex"] = hex.EncodeToString(a.RawEntry)
					a.RawEntry = nil
				}
			}
			if mode == "gm_conflict" {
				want = q.now.Add(3 * time.Hour)
				a.ExpireAt = want
				a.Extra["expiration_source"] = "gm_override"
				a.Extra["expire_time"] = strconv.FormatInt(q.now.Add(4*time.Hour).Unix(), 10)
			}
			id := appendDailyEquipmentSystemMail(t, ctx, f, "r162_source_controls", q.now, a)
			box, _, err := f.group.Mailbox.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			key := strconv.FormatUint(uint64(id), 10)
			m := box.Mails[key]
			switch mode {
			case "missing_source":
				delete(m.Metadata, "source")
			case "missing_marker":
				delete(m.Metadata, "attachment_grant_anchor")
			case "unknown_old_source":
				m.Metadata = map[string]string{"source": "unknown", "system_mail": "true"}
			}
			box.Mails[key] = m
			if err := f.group.Mailbox.Save(ctx, box); err != nil {
				t.Fatal(err)
			}
			ok, code := claimDailyEquipmentMail(t, f, id)
			if ok != wantOK {
				t.Fatalf("mode=%s ack=%t code=%d", mode, ok, code)
			}
			stacks := dailyEquipmentMailStacks(t, ctx, f)
			if !wantOK {
				if len(stacks) != 0 {
					t.Fatal("rejected mail delivered equipment")
				}
				box, _, err = f.group.Mailbox.Load(ctx, "19")
				if err != nil || box.Mails[key].Claimed || box.Mails[key].Deleted || len(box.Mails[key].Attachments) != 1 {
					t.Fatalf("rejected mail consumed: %+v err=%v", box, err)
				}
				return
			}
			if len(stacks) != 1 || !stacks[0].ExpireAt.Equal(want) || stacks[0].Extra["expiration_source"] == currentPVFDailyEquipmentGrantSource || stacks[0].Extra["daily_delete_at"] != "" {
				t.Fatalf("old instance re-granted: %+v want=%s", stacks, want)
			}
			if mode == "gm_conflict" && stacks[0].Extra["expire_time"] != a.Extra["expire_time"] {
				t.Fatal("GM extra rewritten")
			}
		})
	}
}

var errDailyMailInjected = errors.New("r162 mailbox injected failure")

func TestDailyEquipmentRealMailDualRawConflict(t *testing.T) {
	f, q, _, _ := dailyEquipmentFixture(t)
	defer f.service.stopAvatarAppearanceCoalesce(f.session, "mail-dual-raw-end")
	t.Setenv(mailboxClaimTypeRepairEnv, mailboxClaimTypeRepairValueOff)
	for _, mode := range []string{"identical", "deadline", "hex_identity", "raw_identity", "content", "deadline_with_explicit_expiry"} {
		t.Run(mode, func(t *testing.T) {
			ctx := resetDailyEquipmentMailInventory(t, f)
			if err := f.group.Mailbox.Save(ctx, dnfrepo.MailboxRecord{CharacterID: "19"}); err != nil {
				t.Fatal(err)
			}
			deadline := q.now.Add(2 * time.Hour)
			raw := loginWearAnchorRawEntry(9, 400300075, 12345, 0)
			binary.LittleEndian.PutUint32(raw[currentItemListExpireTimeOffset:], uint32(deadline.Unix()))
			other := append([]byte(nil), raw...)
			switch mode {
			case "deadline", "deadline_with_explicit_expiry":
				binary.LittleEndian.PutUint32(other[currentItemListExpireTimeOffset:], uint32(deadline.Add(time.Hour).Unix()))
			case "hex_identity":
				binary.LittleEndian.PutUint32(other[2:], 400300076)
			case "raw_identity":
				binary.LittleEndian.PutUint32(raw[2:], 400300076)
			case "content":
				other[0x12] ^= 1
			}
			a := dnfrepo.MailAttachment{ItemID: 400300075, Count: 1, RawEntry: raw, Extra: map[string]string{"raw_entry_hex": hex.EncodeToString(other)}}
			if mode == "deadline_with_explicit_expiry" {
				a.ExpireAt = deadline
			}
			id := appendDailyEquipmentSystemMail(t, ctx, f, "r162_dual_raw", q.now, a)
			// Settle legacy envelope/type repair separately; snapshot the actual
			// claim inputs, then demand that rejected op95 changes no assets.
			if _, err := f.service.reconcileCurrentMailAttachmentExpirations(ctx, 19); err != nil {
				t.Fatal(err)
			}
			beforeBox, _, err := f.group.Mailbox.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			beforeBox = dnfrepo.CloneMailbox(beforeBox)
			beforeInv, _, err := f.group.Inventory.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			beforeInv = dnfrepo.CloneInventory(beforeInv)
			ok, code := claimDailyEquipmentMail(t, f, id)
			if mode == "identical" {
				stacks := dailyEquipmentMailStacks(t, ctx, f)
				if !ok || len(stacks) != 1 || !stacks[0].ExpireAt.Equal(deadline) || stacks[0].Extra["expiration_source"] == currentPVFDailyEquipmentGrantSource {
					t.Fatalf("identical carriers rejected/renewed: ack=%t code=%d stacks=%+v", ok, code, stacks)
				}
				return
			}
			afterBox, _, err := f.group.Mailbox.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			afterInv, _, err := f.group.Inventory.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			if ok || !reflect.DeepEqual(beforeBox, afterBox) || !reflect.DeepEqual(beforeInv, afterInv) {
				t.Fatalf("dual raw conflict changed assets: mode=%s ack=%t code=%d", mode, ok, code)
			}
			for rest := f.buffer.write.Bytes(); len(rest) > 0; {
				packet, trailing := splitLongHengGameServerUpperPacket(t, rest)
				rest = trailing
				if packet.Header.Classification == 0 && packet.Header.MsgID == 14 {
					t.Fatal("dual raw conflict emitted item refresh")
				}
			}
		})
	}
}

type dailyMailFailInventory struct {
	dnfrepo.InventoryRepository
	hit *int
}

func (r dailyMailFailInventory) Save(context.Context, dnfrepo.InventoryRecord) error {
	*r.hit = *r.hit + 1
	return errDailyMailInjected
}

type dailyMailFailMailbox struct {
	dnfrepo.MailboxRepository
	hit *int
}

func (r dailyMailFailMailbox) Save(context.Context, dnfrepo.MailboxRecord) error {
	*r.hit = *r.hit + 1
	return errDailyMailInjected
}

type dailyMailFailUOW struct {
	dnfrepo.MailboxAssetUnitOfWork
	mode string
	hit  *int
}

func (u dailyMailFailUOW) WithinMailboxAssets(ctx context.Context, assetID, mailID string, apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.MailboxRepository) error) error {
	return u.MailboxAssetUnitOfWork.WithinMailboxAssets(ctx, assetID, mailID, func(c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, m dnfrepo.MailboxRepository) error {
		if u.mode == "inventory_save" {
			i = dailyMailFailInventory{i, u.hit}
		}
		if u.mode == "mailbox_save" {
			m = dailyMailFailMailbox{m, u.hit}
		}
		if err := apply(c, i, m); err != nil {
			return err
		}
		if u.mode == "final_commit" {
			*u.hit = *u.hit + 1
			return errDailyMailInjected
		}
		return nil
	})
}

func TestDailyEquipmentRealMailOp95CommitFaults(t *testing.T) {
	f, q, _, _ := dailyEquipmentFixture(t)
	defer f.service.stopAvatarAppearanceCoalesce(f.session, "mail-commit-controls")
	// Complete the independent legacy pre-claim repair before the fault snapshot.
	// Then disable it so each fault is demonstrably in the op95 claim UOW.
	t.Setenv(mailboxClaimTypeRepairEnv, mailboxClaimTypeRepairValueOff)
	for _, mode := range []string{"inventory_save", "mailbox_save", "final_commit"} {
		t.Run(mode, func(t *testing.T) {
			ctx := resetDailyEquipmentMailInventory(t, f)
			if err := f.group.Mailbox.Save(ctx, dnfrepo.MailboxRecord{CharacterID: "19"}); err != nil {
				t.Fatal(err)
			}
			id := appendDailyEquipmentSystemMail(t, ctx, f, "r162_commit_fault", q.now, dnfrepo.MailAttachment{ItemID: 400300075, Count: 1, Extra: map[string]string{"retained": "yes"}})
			f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return f.group, true }
			if _, err := f.service.reconcileCurrentMailAttachmentExpirations(ctx, 19); err != nil {
				t.Fatal(err)
			}
			beforeBox, _, err := f.group.Mailbox.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			beforeBox = dnfrepo.CloneMailbox(beforeBox)
			beforeInv, _, err := f.group.Inventory.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			beforeInv = dnfrepo.CloneInventory(beforeInv)
			hit := 0
			fault := f.group
			fault.MailboxAssets = dailyMailFailUOW{f.group.MailboxAssets, mode, &hit}
			f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return fault, true }
			ok, _ := claimDailyEquipmentMail(t, f, id)
			afterBox, _, err := f.group.Mailbox.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			afterInv, _, err := f.group.Inventory.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			if ok || hit != 1 || !reflect.DeepEqual(beforeBox, afterBox) || !reflect.DeepEqual(beforeInv, afterInv) {
				t.Fatalf("op95 fault not atomic: mode=%s ack=%t hit=%d boxEqual=%t invEqual=%t", mode, ok, hit, reflect.DeepEqual(beforeBox, afterBox), reflect.DeepEqual(beforeInv, afterInv))
			}
			for rest := f.buffer.write.Bytes(); len(rest) > 0; {
				packet, trailing := splitLongHengGameServerUpperPacket(t, rest)
				rest = trailing
				if packet.Header.Classification == 0 && packet.Header.MsgID == 14 {
					t.Fatal("failed op95 emitted item refresh")
				}
			}
			f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return f.group, true }
			ok, code := claimDailyEquipmentMail(t, f, id)
			stacks := dailyEquipmentMailStacks(t, ctx, f)
			if !ok || len(stacks) != 1 || !stacks[0].ExpireAt.Equal(q.now.Add(time.Minute)) {
				t.Fatalf("retry failed ack=%t code=%d stacks=%+v", ok, code, stacks)
			}
		})
	}
}

func TestDailyEquipmentRealMailWearAndExpiry(t *testing.T) {
	f, q, _, _ := dailyEquipmentFixture(t)
	defer f.service.stopAvatarAppearanceCoalesce(f.session, "mail-wear-end")
	ctx := resetDailyEquipmentMailInventory(t, f)
	deadline := q.now.Add(time.Minute)
	id := appendDailyEquipmentSystemMail(t, ctx, f, "r162_mail_wear", q.now, dnfrepo.MailAttachment{ItemID: 400300075, Count: 1})
	if ok, code := claimDailyEquipmentMail(t, f, id); !ok {
		t.Fatalf("claim rejected %d", code)
	}
	inv, _, err := f.group.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	var slot uint16
	for key, stack := range inv.Slots {
		if stack.ItemID == 400300075 {
			n, e := strconv.ParseUint(key[2:], 10, 16)
			if e != nil {
				t.Fatal(e)
			}
			slot = uint16(n)
		}
	}
	if slot == 0 {
		t.Fatal("missing claimed equipment")
	}
	dailyEquipmentMove(t, f, slot, 3, 19)
	eq, _, err := f.group.Equipment.Load(ctx, "19")
	if err != nil || eq.Entries["19"].ItemID != 400300075 || !eq.Entries["19"].ExpireAt.Equal(deadline) || eq.Entries["19"].Extra["expiration_source"] != currentPVFDailyEquipmentGrantSource {
		t.Fatalf("mail wear lost instance: %+v err=%v", eq, err)
	}
	f.service.armCurrentDailyDeleteClock(f.session)
	task := q.active["daily-delete:"+f.session.connID]
	if task == nil || task.delay != time.Minute {
		t.Fatal("wrong daily timer")
	}
	f.buffer.write.Reset()
	q.now = deadline
	q.fire(task, false)
	eq, _, err = f.group.Equipment.Load(ctx, "19")
	if err != nil || len(eq.Entries) != 0 {
		t.Fatalf("mail worn persists %+v err=%v", eq, err)
	}
	seen := false
	for rest := f.buffer.write.Bytes(); len(rest) > 0; {
		p, trailing := splitLongHengGameServerUpperPacket(t, rest)
		rest = trailing
		if p.Header.MsgID == 14 && p.Header.Classification == 0 && len(p.Body) == 3+currentItemListEntryWireSize && p.Body[0] == 3 {
			seen = binary.LittleEndian.Uint16(p.Body[3:5]) == 19 && binary.LittleEndian.Uint32(p.Body[5:9]) == ^uint32(0)
		}
	}
	if !seen {
		t.Fatal("mail worn expiry missing list3 op14")
	}
	if _, removed, err := f.service.reconcileCurrentDailyDeleteWithRemoved(ctx, f.group, "19", "dnf:10011", f.service.pvfItemCatalog); err != nil || len(removed) != 0 {
		t.Fatalf("expiry replay removed=%v err=%v", removed, err)
	}
}
