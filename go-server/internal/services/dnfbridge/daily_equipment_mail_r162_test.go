package dnfbridge

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func resetDailyEquipmentMailInventory(t *testing.T, f *loginWearAnchorFixture) context.Context {
	t.Helper()
	ctx := context.Background()
	if err := f.group.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       map[string]dnfrepo.ItemStack{},
		Warehouse:   map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.group.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
		AccountID: "dnf:10011",
		Slots:     map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	return ctx
}

func appendDailyEquipmentSystemMail(t *testing.T, ctx context.Context, f *loginWearAnchorFixture, source string, createdAt time.Time, attachments ...dnfrepo.MailAttachment) uint32 {
	t.Helper()
	id, err := dnfrepo.AppendSystemMail(ctx, f.group.Mailbox, dnfrepo.SystemMailDelivery{
		RecipientCharacterID: "19", Source: source, CreatedAt: createdAt, Attachments: attachments,
	})
	if err != nil {
		t.Fatal(err)
	}
	number, err := strconv.ParseUint(id, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	return uint32(number)
}

func claimDailyEquipmentMail(t *testing.T, f *loginWearAnchorFixture, mailID uint32) (bool, byte) {
	t.Helper()
	f.buffer.write.Reset()
	handled, err := p157RealMailClaim(t, f.service, f.session, mailID)
	if err != nil || !handled {
		t.Fatalf("op95 mail=%d handled=%v err=%v", mailID, handled, err)
	}
	return p157RealMailAck(t, f.service, f.session)
}

func dailyEquipmentMailStacks(t *testing.T, ctx context.Context, f *loginWearAnchorFixture) []dnfrepo.ItemStack {
	t.Helper()
	inv, _, err := f.group.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	var stacks []dnfrepo.ItemStack
	for _, stack := range inv.Slots {
		if stack.ItemID == 400300075 {
			stacks = append(stacks, stack)
		}
	}
	return stacks
}

// Exercise the actual mailbox claim path with a real daily-delete template.
// The attachment has the same item-only shape used by system reward delivery;
// no expiration provenance is seeded into it by the test.
func TestDailyEquipmentRealMailClaimRetainsDailyDeadline(t *testing.T) {
	f, q, _, _ := dailyEquipmentFixture(t)
	defer f.service.stopAvatarAppearanceCoalesce(f.session, "daily-mail-end")
	ctx := context.Background()
	if err := f.group.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	id, err := dnfrepo.AppendSystemMail(ctx, f.group.Mailbox, dnfrepo.SystemMailDelivery{
		RecipientCharacterID: "19", Source: "r162_daily_equipment_test", CreatedAt: q.now,
		Attachments: []dnfrepo.MailAttachment{{ItemID: 400300075, Count: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseUint(id, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	f.buffer.write.Reset()
	handled, err := p157RealMailClaim(t, f.service, f.session, uint32(n))
	if err != nil || !handled {
		t.Fatalf("op95 handled=%v err=%v", handled, err)
	}
	ok, code := p157RealMailAck(t, f.service, f.session)
	if !ok {
		t.Fatalf("op95 rejected code=%d", code)
	}
	inv, _, err := f.group.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	var got dnfrepo.ItemStack
	for _, stack := range inv.Slots {
		if stack.ItemID == 400300075 {
			got = stack
		}
	}
	if got.Count != 1 {
		t.Fatalf("claim did not deliver single equipment: %+v", inv.Slots)
	}
	want := q.now.Add(time.Minute)
	if !got.ExpireAt.Equal(want) {
		t.Fatalf("daily mail equipment expiry=%s want original grant boundary=%s source=%q", got.ExpireAt, want, got.Extra["expiration_source"])
	}
	def, err := f.service.pvfItemCatalog.ResolveItem(400300075)
	if err != nil {
		t.Fatal(err)
	}
	if def.Durability > 0 {
		wantDurability := strconv.FormatUint(uint64(def.Durability), 10)
		if got.Extra["durability"] != wantDurability || got.Extra["max_durability"] != wantDurability {
			t.Fatalf("durability evidence was rewritten with expiry: durability=%q max=%q want=%s extra=%+v",
				got.Extra["durability"], got.Extra["max_durability"], wantDurability, got.Extra)
		}
	} else if got.Extra["durability"] != "" || got.Extra["max_durability"] != "" {
		t.Fatalf("zero-durability equipment gained invented durability: %+v", got.Extra)
	}
	if !currentPVFDailyEquipmentGrantRecorded(got, def) {
		t.Fatalf("claim lacks daily cleanup provenance: %+v", got)
	}
	q.now = want
	_, removed, err := f.service.reconcileCurrentDailyDeleteWithRemoved(ctx, f.group, "19", "dnf:10011", f.service.pvfItemCatalog)
	if err != nil || len(removed) != 1 {
		t.Fatalf("at expiry removed=%v err=%v", removed, err)
	}
}

func TestDailyEquipmentRealMailPreservesPerAttachmentEvidence(t *testing.T) {
	f, q, _, _ := dailyEquipmentFixture(t)
	defer f.service.stopAvatarAppearanceCoalesce(f.session, "daily-mail-controls-end")
	ctx := resetDailyEquipmentMailInventory(t, f)
	gmDeadline := q.now.Add(3 * time.Hour)
	gmExtra := map[string]string{
		"expiration_source": "gm_override",
		"gm_expire_days":    "3",
		"expire_time":       strconv.FormatInt(gmDeadline.Unix(), 10),
		"expire_unix":       strconv.FormatInt(gmDeadline.Unix(), 10),
	}
	id := appendDailyEquipmentSystemMail(t, ctx, f, "r162_daily_equipment_mixed", q.now,
		dnfrepo.MailAttachment{ItemID: 400300075, Count: 1},
		dnfrepo.MailAttachment{ItemID: 400300075, Count: 1, ExpireAt: gmDeadline, Extra: gmExtra},
	)
	ok, code := claimDailyEquipmentMail(t, f, id)
	if !ok {
		t.Fatalf("mixed per-attachment op95 rejected code=%d", code)
	}
	stacks := dailyEquipmentMailStacks(t, ctx, f)
	if len(stacks) != 2 {
		t.Fatalf("mixed claim stacks=%d want=2: %+v", len(stacks), stacks)
	}
	dailyBoundary := q.now.Add(time.Minute)
	var sawDaily, sawGM bool
	for _, stack := range stacks {
		switch stack.Extra["expiration_source"] {
		case "runtime_pvf_daily_equipment_grant":
			sawDaily = true
			if !stack.ExpireAt.Equal(dailyBoundary) {
				t.Fatalf("new attachment expiry=%s want=%s", stack.ExpireAt, dailyBoundary)
			}
			if stack.Extra["daily_delete_at"] != strconv.FormatInt(dailyBoundary.Unix(), 10) ||
				stack.Extra["expire_time"] != stack.Extra["daily_delete_at"] ||
				stack.Extra["expire_unix"] != stack.Extra["daily_delete_at"] {
				t.Fatalf("new attachment deadline keys=%+v", stack.Extra)
			}
		case "gm_override":
			sawGM = true
			if !stack.ExpireAt.Equal(gmDeadline) || stack.Extra["gm_expire_days"] != "3" {
				t.Fatalf("GM attachment changed expiry=%s extra=%+v", stack.ExpireAt, stack.Extra)
			}
		default:
			t.Fatalf("unexpected attachment provenance: %+v", stack)
		}
	}
	if !sawDaily || !sawGM {
		t.Fatalf("mixed claim provenance daily=%v gm=%v", sawDaily, sawGM)
	}
	q.now = dailyBoundary
	_, removed, err := f.service.reconcileCurrentDailyDeleteWithRemoved(ctx, f.group, "19", "dnf:10011", f.service.pvfItemCatalog)
	if err != nil || len(removed) != 1 {
		t.Fatalf("at daily boundary removed=%v err=%v", removed, err)
	}
	stacks = dailyEquipmentMailStacks(t, ctx, f)
	if len(stacks) != 1 || stacks[0].Extra["expiration_source"] != "gm_override" || !stacks[0].ExpireAt.Equal(gmDeadline) {
		t.Fatalf("daily cleanup touched GM attachment: %+v", stacks)
	}
}

func TestDailyEquipmentRealMailPreservesTransferDeadlines(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sender string
		source string
	}{
		{name: "old_system_instance", sender: "0", source: "r162_old_system_instance"},
		{name: "player_op94", sender: "42", source: "op94"},
		{name: "auction_delivery", sender: "0", source: "auction_purchase_delivery"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, q, _, _ := dailyEquipmentFixture(t)
			defer f.service.stopAvatarAppearanceCoalesce(f.session, "daily-mail-transfer-end")
			ctx := resetDailyEquipmentMailInventory(t, f)
			deadline := q.now.Add(4 * time.Hour)
			if err := f.group.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
				CharacterID: "19",
				Mails: map[string]dnfrepo.MailRecord{
					"1": {
						MailID:            "1",
						SenderCharacterID: tc.sender,
						CreatedAt:         q.now,
						Metadata:          map[string]string{"source": tc.source},
						Attachments: []dnfrepo.MailAttachment{{
							ItemID: 400300075, Count: 1, ExpireAt: deadline,
						}},
					},
				},
			}); err != nil {
				t.Fatal(err)
			}
			ok, code := claimDailyEquipmentMail(t, f, 1)
			if !ok {
				t.Fatalf("transfer op95 rejected code=%d", code)
			}
			stacks := dailyEquipmentMailStacks(t, ctx, f)
			if len(stacks) != 1 || !stacks[0].ExpireAt.Equal(deadline) || stacks[0].Extra["expiration_source"] != "" {
				t.Fatalf("transfer instance changed: %+v", stacks)
			}
			q.now = q.now.Add(time.Minute)
			_, removed, err := f.service.reconcileCurrentDailyDeleteWithRemoved(ctx, f.group, "19", "dnf:10011", f.service.pvfItemCatalog)
			if err != nil || len(removed) != 0 {
				t.Fatalf("transfer cleanup removed=%v err=%v", removed, err)
			}
		})
	}
}

func TestDailyEquipmentRealMailMissingAnchorRollsBack(t *testing.T) {
	f, _, _, _ := dailyEquipmentFixture(t)
	defer f.service.stopAvatarAppearanceCoalesce(f.session, "daily-mail-anchor-end")
	ctx := resetDailyEquipmentMailInventory(t, f)
	if err := f.group.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "19",
		Mails: map[string]dnfrepo.MailRecord{
			"1": {
				MailID:            "1",
				SenderCharacterID: "0",
				Metadata:          map[string]string{"source": "r162_missing_anchor", "system_mail": "true", "unlimited": "true"},
				Attachments:       []dnfrepo.MailAttachment{{ItemID: 400300075, Count: 1}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	ok, codes := claimDailyEquipmentMail(t, f, 1)
	if ok {
		t.Fatalf("missing-anchor claim accepted code=%d", codes)
	}
	if stacks := dailyEquipmentMailStacks(t, ctx, f); len(stacks) != 0 {
		t.Fatalf("rejected claim delivered %+v", stacks)
	}
	mailbox, found, err := f.group.Mailbox.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load mailbox found=%v err=%v", found, err)
	}
	mail, found := mailbox.Mails["1"]
	if !found || mail.Claimed || mail.Deleted || len(mail.Attachments) != 1 ||
		mail.Attachments[0].ItemID != 400300075 || mail.Attachments[0].Count != 1 || !mail.Attachments[0].ExpireAt.IsZero() {
		t.Fatalf("rejected claim changed mail: found=%v mail=%+v", found, mail)
	}
}

func TestDailyEquipmentRealMailFullBagRetainsMail(t *testing.T) {
	f, q, _, _ := dailyEquipmentFixture(t)
	defer f.service.stopAvatarAppearanceCoalesce(f.session, "daily-mail-full-end")
	ctx := resetDailyEquipmentMailInventory(t, f)
	id := appendDailyEquipmentSystemMail(t, ctx, f, "r162_daily_equipment_full_bag", q.now,
		dnfrepo.MailAttachment{ItemID: 400300075, Count: 1, Extra: map[string]string{"retained": "yes"}})
	if _, err := f.service.reconcileCurrentMailAttachmentExpirations(ctx, 19); err != nil {
		t.Fatal(err)
	}
	beforeBox, _, err := f.group.Mailbox.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	beforeBox = dnfrepo.CloneMailbox(beforeBox)
	full, _, err := f.group.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	full.Slots = map[string]dnfrepo.ItemStack{}
	for slot := int16(3); slot <= 359; slot++ {
		full.Slots["0:"+strconv.Itoa(int(slot))] = dnfrepo.ItemStack{ItemID: 990000000, Count: 1}
	}
	if err := f.group.Inventory.Save(ctx, full); err != nil {
		t.Fatal(err)
	}
	ok, code := claimDailyEquipmentMail(t, f, id)
	if ok {
		t.Fatalf("full-bag op95 accepted code=%d", code)
	}
	mailbox, found, err := f.group.Mailbox.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("full-bag load mailbox found=%v err=%v", found, err)
	}
	mailID := strconv.FormatUint(uint64(id), 10)
	mail, found := mailbox.Mails[mailID]
	if !found || mail.Claimed || mail.Deleted || len(mail.Attachments) != 1 || !mail.Attachments[0].ExpireAt.IsZero() {
		t.Fatalf("full-bag changed mail: found=%v mail=%+v", found, mail)
	}
	if !reflect.DeepEqual(beforeBox.Mails[mailID].Attachments, mail.Attachments) {
		t.Fatal("full-bag projection leaked into retained attachment Extra")
	}
	if stacks := dailyEquipmentMailStacks(t, ctx, f); len(stacks) != 0 {
		t.Fatalf("full-bag delivered %+v", stacks)
	}
	if err := f.group.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       map[string]dnfrepo.ItemStack{},
		Warehouse:   map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	ok, code = claimDailyEquipmentMail(t, f, id)
	if !ok {
		t.Fatalf("claim after making space rejected code=%d", code)
	}
	stacks := dailyEquipmentMailStacks(t, ctx, f)
	if len(stacks) != 1 || !stacks[0].ExpireAt.Equal(q.now.Add(time.Minute)) {
		t.Fatalf("claim after making space: %+v", stacks)
	}
}

func TestDailyEquipmentRealMailAnchorDoesNotSlideToClaimTime(t *testing.T) {
	f, q, _, _ := dailyEquipmentFixture(t)
	defer f.service.stopAvatarAppearanceCoalesce(f.session, "daily-mail-old-anchor-end")
	ctx := resetDailyEquipmentMailInventory(t, f)
	createdAt := q.now.Add(-24 * time.Hour)
	id := appendDailyEquipmentSystemMail(t, ctx, f, "r162_daily_equipment_old_anchor", createdAt,
		dnfrepo.MailAttachment{ItemID: 400300075, Count: 1})
	ok, code := claimDailyEquipmentMail(t, f, id)
	if !ok {
		t.Fatalf("old-anchor claim rejected code=%d", code)
	}
	stacks := dailyEquipmentMailStacks(t, ctx, f)
	want := createdAt.Add(time.Minute)
	if len(stacks) != 1 || !stacks[0].ExpireAt.Equal(want) || stacks[0].ExpireAt.Equal(q.now.Add(time.Minute)) {
		t.Fatalf("claim-time renewal found: %+v want original=%s", stacks, want)
	}
	_, removed, err := f.service.reconcileCurrentDailyDeleteWithRemoved(ctx, f.group, "19", "dnf:10011", f.service.pvfItemCatalog)
	if err != nil || len(removed) != 1 {
		t.Fatalf("old anchor cleanup removed=%v err=%v", removed, err)
	}
}
