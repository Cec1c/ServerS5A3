package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/festival"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
	"time"
)

func TestPrivateFestivalRealPVFCheckout(t *testing.T) {
	path := os.Getenv("PRIVATE_FESTIVAL_PVF")
	if path == "" {
		t.Skip("candidate PVF required")
	}
	archive, e := platformpvf.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	catalog, e := newPVFCeraShopCatalog(archive)
	if e != nil {
		t.Fatal(e)
	}
	if catalog.festivals == nil || len(catalog.festivals.Periods) != 6 || len(catalog.festivals.Commodities) != 273 {
		t.Fatalf("catalog %+v", catalog.festivals)
	}
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.FixedZone("HKT", 8*3600))
	for pid, p := range catalog.festivals.Periods {
		t.Run(p.Name, func(t *testing.T) {
			g := memory.NewMemoryGroup()
			ctx := context.Background()
			account := "festival-test"
			seedCeraShopAccountCera(t, ctx, g, account, 100000000)
			for _, id := range []string{"19", "20"} {
				if e := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, AccountID: account}); e != nil {
					t.Fatal(e)
				}
				if e := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: id, Slots: map[string]repo.ItemStack{}}); e != nil {
					t.Fatal(e)
				}
			}
			svc := &Service{options: options{accountID: account}, pvfItemCatalog: catalog.items, ceraShopCatalog: catalog, repositoryProvider: func() (repo.Group, bool) { return g, true }}
			session := &gameSession{selectedCharacterID: 19}
			svc.gameSessions = map[uint16]*gameSession{19: session, 20: session}
			cid := uint32(0)
			for id, p := range catalog.festivals.Commodities {
				if p == pid {
					cid = id
					break
				}
			}
			product := catalog.products[cid]
			buy := func(serial uint64, n int) (currentCeraShopCommitResult, error) {
				q := currentCeraShopPurchaseRequest{FestivalRequestID: fmt.Sprint(serial), FestivalDigest: fmt.Sprint(n), FestivalSerial: serial}
				q.FestivalAccount = account
				q.FestivalOwner = boundGameSessionCharacter{session: session, character: session.selectedCharacterID, generation: session.characterGeneration}
				for i := 0; i < n; i++ {
					q.Items = append(q.Items, currentCeraShopPurchaseRequestItem{CommodityID: cid})
				}
				return svc.commitCurrentCeraShopPurchase(ctx, session, catalog, q)
			}
			if _, e := buy(1, 3); e != nil {
				t.Fatal(e)
			}
			a, _, _ := g.Account.Load(ctx, account)
			st, e := festival.Load(a.Metadata[festival.MetadataKey])
			if e != nil || st.Counts[pid] != 3 {
				t.Fatal(st, e)
			}
			before := loadCeraShopAccountCera(t, ctx, g, account)
			if before != 100000000-3*int64(product.CeraPrice) {
				t.Fatal("payment mismatch")
			}
			if _, e := buy(1, 3); !errors.Is(e, festival.ErrReplay) {
				t.Fatalf("replay: %v", e)
			}
			if loadCeraShopAccountCera(t, ctx, g, account) != before {
				t.Fatal("replay charged")
			}
			session.selectedCharacterID = 20
			if _, e := buy(2, 23); e != nil {
				t.Fatal(e)
			}
			a, _, _ = g.Account.Load(ctx, account)
			st, e = festival.Load(a.Metadata[festival.MetadataKey])
			if e != nil || st.Counts[pid] != 26 {
				t.Fatal(st, e)
			}
			expected := len(p.Tiers)
			if p.RepeatFrom > 0 {
				expected += int(27 - p.RepeatFrom)
			}
			if len(st.Pending) != expected {
				t.Fatalf("awards %d want %d", len(st.Pending), expected)
			}
			for _, award := range st.Pending {
				def, e := catalog.items.ResolveItem(award.Reward.Item)
				if e != nil || (!def.ExpirationDate.IsZero() && !def.ExpirationDate.After(now)) {
					t.Fatalf("expired/missing tier %+v %v", award, e)
				}
				if award.At <= 3 && award.Character != "19" || award.At > 3 && award.Character != "20" {
					t.Fatal("recipient changed")
				}
			}
			// Simulate a database/process restart: deserialize durable count and outbox.
			raw, _ := festival.Save(st)
			st2, e := festival.Load(raw)
			if e != nil || st2.Counts[pid] != 26 || len(st2.Pending) != expected {
				t.Fatal("restart state")
			}
			// An unrelated account has no purchase progress or shared reward state.
			seedCeraShopAccountCera(t, ctx, g, "other", 0)
			other, _, _ := g.Account.Load(ctx, "other")
			os, _ := festival.Load(other.Metadata[festival.MetadataKey])
			if len(os.Counts) != 0 || len(os.Pending) != 0 {
				t.Fatal("account leak")
			}
			// Insufficient payment must not commit another tier or outbox entry.
			seedCeraShopAccountCera(t, ctx, g, account, 0)
			a, _, _ = g.Account.Load(ctx, account)
			a.Metadata[festival.MetadataKey] = raw
			if e := g.Account.Save(ctx, a); e != nil {
				t.Fatal(e)
			}
			if _, e := buy(3, 1); e == nil {
				t.Fatal("insufficient payment accepted")
			}
			a, _, _ = g.Account.Load(ctx, account)
			if a.Metadata[festival.MetadataKey] != raw {
				t.Fatal("failed checkout changed counter")
			}
			// A failed mailbox write must preserve the paid outbox and roll back
			// every earlier mail write in the same transaction.
			svc.gameSessions = map[uint16]*gameSession{20: session}
			originalAssets := g.MailboxAssets
			g.MailboxAssets = festivalFailMailAssets{originalAssets, originalAssets.(repo.AccountMailboxUnitOfWork)}
			if _, _, err := svc.settlePrivateFestival(ctx, session); err == nil {
				t.Fatal("expected mailbox write failure")
			}
			mail, _, _ := g.Mailbox.Load(ctx, "20")
			if len(mail.Mails) != 0 {
				t.Fatal("partial mailbox commit")
			}
			a, _, _ = g.Account.Load(ctx, account)
			if a.Metadata[festival.MetadataKey] != raw {
				t.Fatal("lost outbox after mail failure")
			}
			g.MailboxAssets = originalAssets
			if _, _, err := svc.settlePrivateFestival(ctx, session); err != nil {
				t.Fatal(err)
			}
			mail, _, _ = g.Mailbox.Load(ctx, "20")
			delivered := len(mail.Mails)
			if _, _, err := svc.settlePrivateFestival(ctx, session); err != nil {
				t.Fatal(err)
			}
			mail, _, _ = g.Mailbox.Load(ctx, "20")
			if len(mail.Mails) != delivered {
				t.Fatal("retry duplicated mail")
			}
			delete(svc.gameSessions, 20)
			session.selectedCharacterID = 19
			session.characterGeneration++
			svc.gameSessions[19] = session
			final, _, err := svc.settlePrivateFestival(ctx, session)
			if err != nil || len(final.Pending) != 0 {
				t.Fatal(final, err)
			}
			mail19, _, _ := g.Mailbox.Load(ctx, "19")
			if len(mail19.Mails)+delivered != expected {
				t.Fatal("lost or wrong-recipient mail")
			}
		})
	}
}

type festivalFailMailAssets struct {
	repo.MailboxAssetUnitOfWork
	owner repo.AccountMailboxUnitOfWork
}

func (f festivalFailMailAssets) WithinAccountMailbox(ctx context.Context, a, c string, apply func(repo.AccountRepository, repo.MailboxRepository) error) error {
	return f.owner.WithinAccountMailbox(ctx, a, c, func(ar repo.AccountRepository, mr repo.MailboxRepository) error {
		return apply(ar, festivalFailMailbox{mr})
	})
}

type festivalFailMailbox struct{ repo.MailboxRepository }

func (f festivalFailMailbox) Save(context.Context, repo.MailboxRecord) error {
	return errors.New("injected mailbox persistence failure")
}

func TestPrivateFestivalPreflightRejectsChangedOwner(t *testing.T) {
	session := &gameSession{selectedCharacterID: 19, characterGeneration: 4}
	svc := &Service{options: options{accountID: "festival-owner"}, gameSessions: map[uint16]*gameSession{19: session}}
	account := &repo.AccountRecord{AccountID: "festival-owner"}
	q := currentCeraShopPurchaseRequest{FestivalRequestID: "request-1", FestivalDigest: "digest-1", FestivalSerial: 1,
		FestivalAccount: account.AccountID, FestivalOwner: boundGameSessionCharacter{session: session, character: 19, generation: 4}}
	if e := svc.privateFestivalPreflight(session, account, q); e != nil {
		t.Fatal(e)
	}
	session.characterGeneration++
	if e := svc.privateFestivalPreflight(session, account, q); !errors.Is(e, festival.ErrInvalid) {
		t.Fatalf("stale generation accepted: %v", e)
	}
	session.characterGeneration--
	other := *account
	other.AccountID = "other-account"
	if e := svc.privateFestivalPreflight(session, &other, q); !errors.Is(e, festival.ErrInvalid) {
		t.Fatalf("other account accepted: %v", e)
	}
	session.loginRevoked.Store(true)
	if e := svc.privateFestivalPreflight(session, account, q); !errors.Is(e, festival.ErrInvalid) {
		t.Fatalf("revoked session accepted: %v", e)
	}
}

func TestPrivateFestivalWireAndCartIsolation(t *testing.T) {
	if units := festivalCartUnits(&pvfCeraShopCatalog{}, currentCeraShopPurchaseRequest{Items: []currentCeraShopPurchaseRequestItem{{CommodityID: 1}}}); len(units) != 0 {
		t.Fatal(units)
	}
	// Packet is always bound to the server-selected character, never request IDs.
	b := encodePrivateFestival(festival.State{}, nil, 19, 7, 99, 5)
	if len(b) != 1568 || string(b[:4]) != "FES1" {
		t.Fatal("wire size")
	}
}
