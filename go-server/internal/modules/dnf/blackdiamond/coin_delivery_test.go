package blackdiamond

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func coinTestFixture(t *testing.T, credited int64) (context.Context, dnfrepo.Group, time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, time.November, 14, 13, 0, 0, 0, time.UTC)
	g := memory.NewMemoryGroup()
	if err := g.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account", Metadata: map[string]string{
		premium.MetadataKey(premium.TypeBlackDiamond): "1", // already expired: delivery still owes pending coins
	}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "7", AccountID: "account", Stats: map[string]int64{
			premium.BlackDiamondFatigueCreditedTotalKey:  credited,
			premium.BlackDiamondFatigueDeliveredTotalKey: 0,
		},
	}); err != nil {
		t.Fatal(err)
	}
	return ctx, g, now
}

func validCoinTemplate() dnfrepo.ItemStack {
	return dnfrepo.ItemStack{
		ItemID: premium.BlackDiamondCoinItemID,
		Count:  1,
		Extra: map[string]string{
			"source":         "caller_must_not_override",
			"item_kind":      "stackable",
			"pvf_path":       "stackable/event/vendingmachine/pcroomtoken_vendingmachine.stk",
			"stackable_type": "[material]",
			"stack_limit":    "1000",
		},
	}
}

func loadCoinCharacter(t *testing.T, ctx context.Context, g dnfrepo.Group) dnfrepo.CharacterRecord {
	t.Helper()
	character, found, err := g.Character.Load(ctx, "7")
	if err != nil || !found {
		t.Fatalf("character found=%t err=%v", found, err)
	}
	return character
}

func loadCoinMailbox(t *testing.T, ctx context.Context, g dnfrepo.Group) dnfrepo.MailboxRecord {
	t.Helper()
	mailbox, found, err := g.Mailbox.Load(ctx, "7")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		return dnfrepo.MailboxRecord{CharacterID: "7"}
	}
	return mailbox
}

func TestDeliverCoinsBatchesPendingAfterExpiryAndBoundedRetry(t *testing.T) {
	ctx, g, now := coinTestFixture(t, 32)

	first, err := DeliverBlackDiamondCoins(ctx, g, "account", "7", now, validCoinTemplate())
	if err != nil || !first {
		t.Fatalf("first delivered=%t err=%v", first, err)
	}
	character := loadCoinCharacter(t, ctx, g)
	if got := character.Stats[premium.BlackDiamondFatigueDeliveredTotalKey]; got != 31 {
		t.Fatalf("delivered after first=%d want=31", got)
	}
	mailbox := loadCoinMailbox(t, ctx, g)
	if len(mailbox.Mails) != 1 {
		t.Fatalf("mail count=%d want=1", len(mailbox.Mails))
	}
	var mail dnfrepo.MailRecord
	for _, candidate := range mailbox.Mails {
		mail = candidate
	}
	if len(mail.Attachments) != 1 || mail.Attachments[0].ItemID != premium.BlackDiamondCoinItemID || mail.Attachments[0].Count != 31 {
		t.Fatalf("attachment=%+v", mail.Attachments)
	}
	if mail.Metadata["source"] != "黑钻疲劳奖励" || mail.Attachments[0].Extra["source"] != "black_diamond_fatigue_reward" {
		t.Fatalf("source metadata=%+v/%+v", mail.Metadata, mail.Attachments[0].Extra)
	}

	second, err := DeliverBlackDiamondCoins(ctx, g, "account", "7", now.Add(time.Minute), validCoinTemplate())
	if err != nil || !second {
		t.Fatalf("second delivered=%t err=%v", second, err)
	}
	character = loadCoinCharacter(t, ctx, g)
	if got := character.Stats[premium.BlackDiamondFatigueDeliveredTotalKey]; got != 32 {
		t.Fatalf("delivered after second=%d want=32", got)
	}
	third, err := DeliverBlackDiamondCoins(ctx, g, "account", "7", now.Add(2*time.Minute), validCoinTemplate())
	if err != nil || third {
		t.Fatalf("settled retry delivered=%t err=%v", third, err)
	}
	if got := len(loadCoinMailbox(t, ctx, g).Mails); got != 2 {
		t.Fatalf("retry created duplicate mail: %d", got)
	}
}

func TestDeliverCoinsFullMailboxLeavesDeliveredTotalUnchanged(t *testing.T) {
	ctx, g, now := coinTestFixture(t, 1)
	mails := make(map[string]dnfrepo.MailRecord, NormalMailboxCapacity)
	for i := 0; i < NormalMailboxCapacity; i++ {
		id := strings.Repeat("1", i+1)
		mails[id] = dnfrepo.MailRecord{MailID: id, CreatedAt: now.Add(-time.Hour)}
	}
	if err := g.Mailbox.Save(ctx, dnfrepo.MailboxRecord{CharacterID: "7", Mails: mails}); err != nil {
		t.Fatal(err)
	}
	delivered, err := DeliverBlackDiamondCoins(ctx, g, "account", "7", now, validCoinTemplate())
	if delivered || !errors.Is(err, ErrMailboxFull) {
		t.Fatalf("delivered=%t error=%v", delivered, err)
	}
	character := loadCoinCharacter(t, ctx, g)
	if got := character.Stats[premium.BlackDiamondFatigueDeliveredTotalKey]; got != 0 {
		t.Fatalf("full mailbox changed delivered=%d", got)
	}
	if got := len(loadCoinMailbox(t, ctx, g).Mails); got != NormalMailboxCapacity {
		t.Fatalf("mail count=%d want=%d", got, NormalMailboxCapacity)
	}
}

type coinMailboxFailure struct {
	dnfrepo.MailboxRepository
	err error
}

func (r coinMailboxFailure) Save(context.Context, dnfrepo.MailboxRecord) error { return r.err }

type coinMailboxOverrideAssets struct {
	dnfrepo.AccountCharacterAssetUnitOfWork
	mailbox dnfrepo.MailboxRepository
}

func (u coinMailboxOverrideAssets) WithinAccountCharacterMailboxAssets(
	ctx context.Context,
	accountID, characterID string,
	apply func(
		dnfrepo.AccountInventoryRepository,
		dnfrepo.CharacterRepository,
		dnfrepo.InventoryRepository,
		dnfrepo.EquipmentRepository,
		dnfrepo.MailboxRepository,
	) error,
) error {
	base, ok := u.AccountCharacterAssetUnitOfWork.(dnfrepo.AccountCharacterMailboxAssetUnitOfWork)
	if !ok {
		return dnfrepo.ErrAccountCharacterAssetTransactionUnavailable
	}
	return base.WithinAccountCharacterMailboxAssets(ctx, accountID, characterID, func(
		a dnfrepo.AccountInventoryRepository,
		c dnfrepo.CharacterRepository,
		i dnfrepo.InventoryRepository,
		e dnfrepo.EquipmentRepository,
		_ dnfrepo.MailboxRepository,
	) error {
		return apply(a, c, i, e, u.mailbox)
	})
}

func TestDeliverCoinsMailFailureRollsBackDeliveredTotal(t *testing.T) {
	ctx, g, now := coinTestFixture(t, 1)
	boom := errors.New("mail write failed")
	g.AccountAssets = coinMailboxOverrideAssets{
		AccountCharacterAssetUnitOfWork: g.AccountAssets,
		mailbox:                         coinMailboxFailure{MailboxRepository: g.Mailbox, err: boom},
	}
	delivered, err := DeliverBlackDiamondCoins(ctx, g, "account", "7", now, validCoinTemplate())
	if delivered || !errors.Is(err, boom) {
		t.Fatalf("delivered=%t error=%v", delivered, err)
	}
	character := loadCoinCharacter(t, ctx, g)
	if got := character.Stats[premium.BlackDiamondFatigueDeliveredTotalKey]; got != 0 {
		t.Fatalf("mail failure committed delivered=%d", got)
	}
	if got := len(loadCoinMailbox(t, ctx, g).Mails); got != 0 {
		t.Fatalf("mail failure committed %d mails", got)
	}
}

func TestDeliverCoinsRejectsCharacterOutsideAccount(t *testing.T) {
	ctx, g, now := coinTestFixture(t, 1)
	if err := g.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "foreign"}); err != nil {
		t.Fatal(err)
	}
	delivered, err := DeliverBlackDiamondCoins(ctx, g, "foreign", "7", now, validCoinTemplate())
	if delivered || !errors.Is(err, dnfrepo.ErrAccountCharacterMailboxOwnership) {
		t.Fatalf("delivered=%t error=%v", delivered, err)
	}
	if got := loadCoinCharacter(t, ctx, g).Stats[premium.BlackDiamondFatigueDeliveredTotalKey]; got != 0 {
		t.Fatalf("foreign owner changed delivered=%d", got)
	}
	if got := len(loadCoinMailbox(t, ctx, g).Mails); got != 0 {
		t.Fatalf("foreign owner created %d mails", got)
	}
}

func TestDeliverCoinsRejectsFabricatedTemplates(t *testing.T) {
	ctx, g, now := coinTestFixture(t, 1)
	validRaw := make([]byte, coinTemplateRawSize)
	binary.LittleEndian.PutUint32(validRaw[2:6], uint32(premium.BlackDiamondCoinItemID))
	binary.LittleEndian.PutUint32(validRaw[6:10], 1)
	foreignRaw := append([]byte(nil), validRaw...)
	binary.LittleEndian.PutUint32(foreignRaw[2:6], 1001)
	for _, tc := range []struct {
		name string
		coin dnfrepo.ItemStack
	}{
		{name: "wrong item", coin: func() dnfrepo.ItemStack { c := validCoinTemplate(); c.ItemID = 1001; return c }()},
		{name: "wrong template count", coin: func() dnfrepo.ItemStack { c := validCoinTemplate(); c.Count = 2; return c }()},
		{name: "bound coin", coin: func() dnfrepo.ItemStack { c := validCoinTemplate(); c.Bind = true; return c }()},
		{name: "equipment metadata", coin: func() dnfrepo.ItemStack { c := validCoinTemplate(); c.Extra["equipment_type"] = "[weapon]"; return c }()},
		{name: "equipment path", coin: func() dnfrepo.ItemStack {
			c := validCoinTemplate()
			c.Extra["pvf_path"] = "equipment/character/swordman/weapon/test.equ"
			return c
		}()},
		{name: "foreign raw item", coin: func() dnfrepo.ItemStack { c := validCoinTemplate(); c.RawEntry = foreignRaw; return c }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			delivered, err := DeliverBlackDiamondCoins(ctx, g, "account", "7", now, tc.coin)
			if delivered || !errors.Is(err, ErrCoinTemplateInvalid) {
				t.Fatalf("delivered=%t error=%v", delivered, err)
			}
		})
	}
	if got := loadCoinCharacter(t, ctx, g).Stats[premium.BlackDiamondFatigueDeliveredTotalKey]; got != 0 {
		t.Fatalf("invalid templates changed delivered=%d", got)
	}
}
