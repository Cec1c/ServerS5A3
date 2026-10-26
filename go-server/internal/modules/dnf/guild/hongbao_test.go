package guild

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/itemexpiration"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const hongbaoFixture = `[hongbao]
490003263 103938 50000 100 490003264 103939 20000 95 490003265 103940 10000 90 490003266 103941 5000 80
[/hongbao]
[min guild member count]
30
[max total cera per day]
50000
[max take count per day]
5`

func hongbaoFixtureContract(t *testing.T) HongbaoContract {
	t.Helper()
	catalog, err := ParseHongbaoCatalog(hongbaoFixture)
	if err != nil {
		t.Fatal(err)
	}
	return HongbaoContract{Catalog: catalog, RandIntn: func(n uint32) (uint32, error) { return n / 2, nil }, GrantItem: func(_ context.Context, _ r.Group, _ *r.CharacterRecord, inv *r.InventoryRecord, p HongbaoProduct, expires time.Time) (string, error) {
		if inv.Slots == nil {
			inv.Slots = map[string]r.ItemStack{}
		}
		for i := 9; i < 100; i++ {
			key := fmt.Sprintf("0:%d", i)
			if _, used := inv.Slots[key]; !used {
				inv.Slots[key] = r.ItemStack{ItemID: p.ItemID, Count: 1, ExpireAt: expires, Extra: map[string]string{"expire_time": strconv.FormatInt(expires.Unix(), 10)}}
				return key, nil
			}
		}
		return "", errors.New("fixture inventory full")
	}, DeliverMail: hongbaoFixtureMail}
}

// This tests the domain-to-mail contract and the real transactional mailbox.
// The refund item is an explicit test-only adapter, not a production PVF ID.
// Bridge integration supplies the matched TokenCera attachment projection.
func hongbaoFixtureMail(ctx context.Context, tx r.Group, actor r.CharacterRecord, a HongbaoMailAward, now time.Time) ([]string, error) {
	var attachments []r.MailAttachment
	if a.Coins > 0 {
		attachments = append(attachments, r.MailAttachment{ItemID: ContributionCoinID, Count: int64(a.Coins)})
	}
	if a.RefundTokenCera > 0 {
		attachments = append(attachments, r.MailAttachment{ItemID: 900000001, Count: int64(a.RefundTokenCera), Extra: map[string]string{"fixture_currency": "TokenCera"}})
	}
	id, err := r.AppendSystemMail(ctx, tx.Mailbox, r.SystemMailDelivery{RecipientCharacterID: actor.CharacterID, RecipientName: actor.Name, Source: "guild_hongbao", CompletionKey: fmt.Sprint(a.RecordID), Title: "红包结算", Attachments: attachments, CreatedAt: now})
	if err != nil {
		return nil, err
	}
	return []string{id}, nil
}

func hongbaoGroup(t *testing.T) (r.Group, *HongbaoOwner, time.Time) {
	t.Helper()
	ctx := context.Background()
	repos := guildTestGroup(t, 32)
	reg := NewRegistry()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= 32; i++ {
		ch, _, err := repos.Character.Load(ctx, fmt.Sprint(i))
		if err != nil {
			t.Fatal(err)
		}
		account := r.AccountRecord{AccountID: ch.AccountID}
		cerashop.SetBalance(&account, 200000)
		cerashop.SetTokenCeraBalance(&account, 123)
		cerashop.SetHappyTokenCeraBalance(&account, 456)
		if err := repos.Account.Save(ctx, account); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			if _, err := reg.Create(ch, "红包测试", now.Add(-48*time.Hour)); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := reg.JoinDirect(1, ch, now.Add(-24*time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	owner, err := NewHongbaoOwner(repos, hongbaoFixtureContract(t))
	if err != nil {
		t.Fatal(err)
	}
	return repos, owner, now
}
func hongbaoRequest(character int, op string) HongbaoRequest {
	return HongbaoRequest{Owner: r.GuildOwner{AccountID: fmt.Sprint("a", character), CharacterID: fmt.Sprint(character)}, GuildID: 1, OperationID: op, Revalidate: func() bool { return true }}
}
func hongbaoIssue(t *testing.T, o *HongbaoOwner, now time.Time, op string, commodity uint32, parts uint8) HongbaoIssueResult {
	t.Helper()
	purchase, err := o.Purchase(context.Background(), HongbaoPurchaseRequest{hongbaoRequest(1, "buy-"+op), commodity}, now)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := o.Issue(context.Background(), HongbaoIssueRequest{hongbaoRequest(1, "issue-"+op), purchase.Product.ItemID, parts}, now)
	if err != nil {
		t.Fatal(err)
	}
	return issue
}
func hongbaoClaim(t *testing.T, o *HongbaoOwner, now time.Time, id uint32, who int) HongbaoClaimResult {
	t.Helper()
	res, err := o.Claim(context.Background(), HongbaoClaimRequest{hongbaoRequest(who, fmt.Sprintf("claim-%d-%d", id, who)), id}, now)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestHongbaoCatalog(t *testing.T) {
	c, err := ParseHongbaoCatalog(hongbaoFixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Products()) != 4 || c.minMembers != 30 || c.purchaseLimit != 50000 || c.claimLimit != 5 {
		t.Fatal(c)
	}
	for i, p := range c.Products() {
		if p.ItemID != int64(490003263+i) || p.CommodityID != uint32(103938+i) {
			t.Fatal(p)
		}
	}
	changed := c.Products()
	changed[0].Price = 0
	if c.Products()[0].Price == 0 {
		t.Fatal("mutable catalog exposed")
	}
	for i, text := range []string{"", strings.Replace(hongbaoFixture, "50000 100", "-1 100", 1), strings.Replace(hongbaoFixture, "50000 100", "50000 101", 1), strings.Replace(hongbaoFixture, "103939", "103938", 1), strings.Replace(hongbaoFixture, "490003264", "490003263", 1), strings.Replace(hongbaoFixture, "30\n", "0\n", 1), strings.Replace(hongbaoFixture, "5", "`5`", 1), hongbaoFixture + "\n[max take count per day]\n5", strings.Replace(hongbaoFixture, "[max take count per day]\n5", "", 1)} {
		t.Run(fmt.Sprint("bad-", i), func(t *testing.T) {
			if _, err := ParseHongbaoCatalog(text); err == nil {
				t.Fatal("malformed catalog accepted")
			}
		})
	}
}
func TestHongbaoRealExtractedCatalog(t *testing.T) {
	path := os.Getenv("GUILD_HONGBAO_EXTRACTED_ETC")
	if path == "" {
		t.Skip("set GUILD_HONGBAO_EXTRACTED_ETC to the matched extracted file")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := ParseHongbaoCatalog(string(b))
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := ParseHongbaoCatalog(hongbaoFixture)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal(actual, expected)
	}
}

func TestHongbaoPurchaseFourTiersAndAtomicProjection(t *testing.T) {
	for i, price := range []uint32{50000, 20000, 10000, 5000} {
		t.Run(fmt.Sprint(price), func(t *testing.T) {
			repos, o, now := hongbaoGroup(t)
			q := HongbaoPurchaseRequest{hongbaoRequest(1, "first"), uint32(103938 + i)}
			res, err := o.Purchase(context.Background(), q, now)
			if err != nil {
				t.Fatal(err)
			}
			if res.Product.Price != price || res.Wallet.Cera != 200000-int64(price) || res.Wallet.TokenCera != 123 || res.Wallet.HappyTokenCera != 456 || !res.ExpireAt.Equal(now.Add(240*time.Hour)) {
				t.Fatal(res)
			}
			inv, _, _ := repos.Inventory.Load(context.Background(), "1")
			if len(inv.Slots) != 1 || inv.Slots[res.Slot].Count != 1 {
				t.Fatal(inv)
			}
			view, err := o.View(context.Background(), q.Owner, now)
			if err != nil || view.PurchasedToday != price {
				t.Fatal(view, err)
			}
			repeat, err := o.Purchase(context.Background(), q, now.Add(time.Minute))
			if err != nil || !repeat.Replayed || repeat.Slot != res.Slot {
				t.Fatal(repeat, err)
			}
			q.CommodityID = 103941
			if i == 3 {
				q.CommodityID = 103940
			}
			if _, err := o.Purchase(context.Background(), q, now); !errors.Is(err, ErrHongbaoIdentity) {
				t.Fatal(err)
			}
		})
	}
	for _, mode := range []string{"full", "no_expiry", "conflicting_expiry", "overwrite_other", "wrong_item", "wallet"} {
		t.Run(mode, func(t *testing.T) {
			repos, o, now := hongbaoGroup(t)
			original := o.contract.GrantItem
			if mode == "wallet" {
				a, _, _ := repos.Account.Load(context.Background(), "a1")
				cerashop.SetBalance(&a, 0)
				repos.Account.Save(context.Background(), a)
			} else {
				o.contract.GrantItem = func(ctx context.Context, tx r.Group, ch *r.CharacterRecord, inv *r.InventoryRecord, p HongbaoProduct, e time.Time) (string, error) {
					if mode == "full" {
						return "", errors.New("full")
					}
					key, err := original(ctx, tx, ch, inv, p, e)
					if err != nil {
						return key, err
					}
					s := inv.Slots[key]
					switch mode {
					case "no_expiry":
						s.ExpireAt = time.Time{}
					case "conflicting_expiry":
						s.Extra["expire_time"] = "1"
					case "wrong_item":
						s.ItemID++
					case "overwrite_other":
						inv.Slots["0:200"] = r.ItemStack{ItemID: 42, Count: 1}
					}
					inv.Slots[key] = s
					return key, nil
				}
			}
			before := hongbaoSnapshot(t, repos)
			if _, err := o.Purchase(context.Background(), HongbaoPurchaseRequest{hongbaoRequest(1, "bad"), 103941}, now); err == nil {
				t.Fatal("invalid purchase succeeded")
			}
			if !reflect.DeepEqual(before, hongbaoSnapshot(t, repos)) {
				t.Fatal("purchase failed non-atomically")
			}
		})
	}
}

func TestHongbaoMemberCountDailyLimitAndSixAM(t *testing.T) {
	repos, o, now := hongbaoGroup(t)
	ctx := context.Background()
	reg, _ := Load(ctx, repos.Settings)
	for _, id := range []string{"30", "31", "32"} {
		delete(reg.Guilds[1].Members, id)
	}
	Save(ctx, repos.Settings, reg, now)
	q := HongbaoPurchaseRequest{hongbaoRequest(1, "same-op"), 103938}
	if _, err := o.Purchase(ctx, q, now); !errors.Is(err, ErrHongbaoMemberCount) {
		t.Fatal(err)
	}
	ch, _, _ := repos.Character.Load(ctx, "30")
	reg.JoinDirect(1, ch, now)
	Save(ctx, repos.Settings, reg, now)
	if _, err := o.Purchase(ctx, q, now); err != nil {
		t.Fatal(err)
	}
	q.OperationID = "next"
	if _, err := o.Purchase(ctx, q, now); !errors.Is(err, ErrHongbaoPurchaseLimit) {
		t.Fatal(err)
	}
	boundary := itemexpiration.NextDailyDeleteAt(now)
	if _, err := o.Purchase(ctx, q, boundary.Add(-time.Nanosecond)); !errors.Is(err, ErrHongbaoPurchaseLimit) {
		t.Fatal(err)
	}
	if _, err := o.Purchase(ctx, q, boundary); err != nil {
		t.Fatal(err)
	}
	// Joining-day restriction belongs to attendance, not hongbao.
	q = HongbaoPurchaseRequest{hongbaoRequest(30, "join-day"), 103941}
	if _, err := o.Purchase(ctx, q, now); err != nil {
		t.Fatal(err)
	}
}

func TestHongbaoIssueIdentityBoundariesAndPersistedShares(t *testing.T) {
	repos, o, now := hongbaoGroup(t)
	ctx := context.Background()
	a, err := o.Purchase(ctx, HongbaoPurchaseRequest{hongbaoRequest(1, "buy1"), 103941}, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := o.Purchase(ctx, HongbaoPurchaseRequest{hongbaoRequest(1, "buy2"), 103941}, now)
	if err != nil {
		t.Fatal(err)
	}
	if a.Slot == b.Slot {
		t.Fatal(a, b)
	}
	q := HongbaoIssueRequest{hongbaoRequest(1, "issue"), a.Product.ItemID, 5}
	for _, parts := range []uint8{4, 31} {
		q.Portions = parts
		if _, err := o.Issue(ctx, q, now); !errors.Is(err, ErrHongbaoRequest) {
			t.Fatal(err)
		}
	}
	q.Portions = 5
	first, err := o.Issue(ctx, q, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.ConsumedSlot != a.Slot {
		t.Fatal(first)
	}
	reg, _ := Load(ctx, repos.Settings)
	shares := append([]uint32(nil), reg.Hongbao.Records[first.RecordID].Shares...)
	restarted, err := NewHongbaoOwner(repos, hongbaoFixtureContract(t))
	if err != nil {
		t.Fatal(err)
	}
	restarted.contract.RandIntn = func(uint32) (uint32, error) { return 0, errors.New("must not reroll replay") }
	replay, err := restarted.Issue(ctx, q, now.Add(time.Hour))
	if err != nil || !replay.Replayed || replay.RecordID != first.RecordID {
		t.Fatal(replay, err)
	}
	q.Portions = 30
	if _, err := o.Issue(ctx, q, now); !errors.Is(err, ErrHongbaoIdentity) {
		t.Fatal(err)
	}
	q.OperationID = "issue2"
	second, err := o.Issue(ctx, q, now)
	if err != nil || second.RecordID == first.RecordID || second.ConsumedSlot != b.Slot {
		t.Fatal(second, err)
	}
	reg, _ = Load(ctx, repos.Settings)
	if !reflect.DeepEqual(shares, reg.Hongbao.Records[first.RecordID].Shares) || len(reg.Hongbao.Records[second.RecordID].Shares) != 30 {
		t.Fatal(reg.Hongbao)
	}
	for _, h := range reg.Hongbao.Records {
		var total uint32
		for _, x := range h.Shares {
			if x == 0 {
				t.Fatal(h)
			}
			total += x
		}
		if total != h.Product.Price {
			t.Fatal(h)
		}
	}
	inv, _, _ := repos.Inventory.Load(ctx, "1")
	if len(inv.Slots) != 0 {
		t.Fatal(inv)
	}
}

func TestHongbaoIssueInventoryEligibilityAndScope(t *testing.T) {
	for _, mode := range []string{"expired", "no_expiry", "locked", "pending_unlock", "malformed_expiry", "conflicting_expiry", "stack_two", "warehouse", "avatar", "negative_slot", "wrong_owner", "stale_session"} {
		t.Run(mode, func(t *testing.T) {
			repos, o, now := hongbaoGroup(t)
			ctx := context.Background()
			p, err := o.Purchase(ctx, HongbaoPurchaseRequest{hongbaoRequest(1, "buy"), 103941}, now)
			if err != nil {
				t.Fatal(err)
			}
			inv, _, _ := repos.Inventory.Load(ctx, "1")
			s := inv.Slots[p.Slot]
			switch mode {
			case "expired":
				now = p.ExpireAt
			case "no_expiry":
				s.ExpireAt = time.Time{}
				s.Extra = nil
			case "locked":
				s.Extra["equipment_lock_state"] = "1"
			case "pending_unlock":
				s.Extra["equipment_lock_state"] = "2"
				s.Extra["equipment_unlock_at"] = strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
			case "malformed_expiry":
				s.Extra["expire_time"] = "x"
			case "conflicting_expiry":
				s.Extra["expire_time"] = "1"
			case "stack_two":
				s.Count = 2
			}
			inv.Slots[p.Slot] = s
			switch mode {
			case "warehouse":
				inv.Warehouse = map[string]r.ItemStack{"2:0": s}
				delete(inv.Slots, p.Slot)
			case "avatar":
				inv.Slots["1:9"] = s
				delete(inv.Slots, p.Slot)
			case "negative_slot":
				inv.Slots["0:-1"] = s
				delete(inv.Slots, p.Slot)
			}
			repos.Inventory.Save(ctx, inv)
			before := hongbaoSnapshot(t, repos)
			q := HongbaoIssueRequest{hongbaoRequest(1, "issue"), p.Product.ItemID, 5}
			if mode == "wrong_owner" {
				q.Owner.AccountID = "a2"
			}
			if mode == "stale_session" {
				q.Revalidate = func() bool { return false }
			}
			if _, err := o.Issue(ctx, q, now); err == nil {
				t.Fatal("invalid instance issued")
			}
			if !reflect.DeepEqual(before, hongbaoSnapshot(t, repos)) {
				t.Fatal("rejection mutated state")
			}
		})
	}
}

func TestHongbaoAccountQuotaAcrossCharactersAndDay(t *testing.T) {
	repos, o, now := hongbaoGroup(t)
	ctx := context.Background()
	ch, _, _ := repos.Character.Load(ctx, "3")
	ch.AccountID = "a2"
	repos.Character.Save(ctx, ch)
	reg, _ := Load(ctx, repos.Settings)
	m := reg.Guilds[1].Members["3"]
	m.AccountID = "a2"
	reg.Guilds[1].Members["3"] = m
	Save(ctx, repos.Settings, reg, now)
	var ids []uint32
	for i := 0; i < 6; i++ {
		ids = append(ids, hongbaoIssue(t, o, now, fmt.Sprint(i), 103941, 5).RecordID)
	}
	for i := 0; i < 5; i++ {
		hongbaoClaim(t, o, now, ids[i], 2)
	}
	q := HongbaoClaimRequest{hongbaoRequest(3, "sixth"), ids[5]}
	q.Owner.AccountID = "a2"
	if _, err := o.Claim(ctx, q, now); !errors.Is(err, ErrHongbaoClaimLimit) {
		t.Fatal(err)
	}
	q.RecordID = ids[0]
	if _, err := o.Claim(ctx, q, now); !errors.Is(err, ErrHongbaoAlreadyClaimed) {
		t.Fatal(err)
	}
	q.RecordID = ids[5]
	if _, err := o.Claim(ctx, q, itemexpiration.NextDailyDeleteAt(now)); err != nil {
		t.Fatal(err)
	}
}

func TestHongbaoConcurrentLastShareAndReceiptReplay(t *testing.T) {
	repos, o, now := hongbaoGroup(t)
	ctx := context.Background()
	id := hongbaoIssue(t, o, now, "race", 103941, 5).RecordID
	var total uint32
	for i := 2; i <= 5; i++ {
		total += hongbaoClaim(t, o, now, id, i).Amount
	}
	var wg sync.WaitGroup
	results := make(chan HongbaoClaimResult, 16)
	errs := make(chan error, 16)
	for i := 6; i < 22; i++ {
		wg.Go(func() {
			res, err := o.Claim(ctx, HongbaoClaimRequest{hongbaoRequest(i, fmt.Sprint("race", i)), id}, now)
			results <- res
			errs <- err
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrHongbaoUnavailable) {
			t.Fatal(err)
		}
	}
	for res := range results {
		total += res.Amount
	}
	if success != 1 || total != 5000 {
		t.Fatal(success, total)
	}
	q := HongbaoClaimRequest{hongbaoRequest(2, fmt.Sprintf("claim-%d-%d", id, 2)), id}
	res, err := o.Claim(ctx, q, now.Add(time.Hour))
	if err != nil || !res.Replayed {
		t.Fatal(res, err)
	}
	q.RecordID++
	if _, err := o.Claim(ctx, q, now); !errors.Is(err, ErrHongbaoIdentity) {
		t.Fatal(err)
	}
	var once sync.WaitGroup
	settled := make(chan HongbaoSettlementResult, 10)
	settleErr := make(chan error, 10)
	for range 10 {
		once.Go(func() { s, e := o.Settle(ctx, id, now); settled <- s; settleErr <- e })
	}
	once.Wait()
	close(settled)
	close(settleErr)
	fresh := 0
	for s := range settled {
		if !s.Replayed {
			fresh++
		}
		if s.Coins != 40 || s.Points != 40 || s.RefundTokenCera != 0 || s.Sender != hongbaoRequest(1, "").Owner {
			t.Fatal(s)
		}
	}
	for e := range settleErr {
		if e != nil {
			t.Fatal(e)
		}
	}
	if fresh != 1 {
		t.Fatal(fresh)
	}
	mailbox, _, _ := repos.Mailbox.Load(ctx, "1")
	if len(mailbox.Mails) != 1 {
		t.Fatal(mailbox)
	}
}

func TestHongbaoExpiryMembershipAndReadOnlyViews(t *testing.T) {
	for _, mode := range []string{"same", "left", "rejoined", "new_guild", "dissolved"} {
		t.Run(mode, func(t *testing.T) {
			repos, o, now := hongbaoGroup(t)
			ctx := context.Background()
			id := hongbaoIssue(t, o, now, "partial", 103938, 5).RecordID
			claim := hongbaoClaim(t, o, now, id, 2)
			before := hongbaoSnapshot(t, repos)
			view, err := o.View(ctx, hongbaoRequest(2, "").Owner, now)
			if err != nil || len(view.Records) != 1 {
				t.Fatal(view, err)
			}
			history, err := o.History(ctx, hongbaoRequest(2, "").Owner, id, now)
			if err != nil || len(history) != 1 || history[0].Amount != claim.Amount {
				t.Fatal(history, err)
			}
			view.Records[0].Shares[0] = 999
			history[0].Amount = 999
			if !reflect.DeepEqual(before, hongbaoSnapshot(t, repos)) {
				t.Fatal("query awarded or exposed mutable state")
			}
			if _, err := o.Settle(ctx, id, now); !errors.Is(err, ErrHongbaoNotReady) {
				t.Fatal(err)
			}
			reg, _ := Load(ctx, repos.Settings)
			g := reg.Guilds[1]
			if mode == "rejoined" {
				m := g.Members["1"]
				m.JoinedAt = now.Add(time.Second)
				g.Members["1"] = m
			}
			if mode == "left" || mode == "new_guild" {
				delete(g.Members, "1")
				g.MasterCharacterID = "2"
				m := g.Members["2"]
				m.Grade = 1
				g.Members["2"] = m
			}
			if mode == "new_guild" {
				ch, _, _ := repos.Character.Load(ctx, "1")
				reg.Create(ch, "新公会", now.Add(time.Second))
			}
			if mode == "dissolved" {
				delete(reg.Guilds, 1)
			}
			if err := Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			at := now.Add(24 * time.Hour)
			ids, err := o.Pending(ctx, at, 10)
			if err != nil || !reflect.DeepEqual(ids, []uint32{id}) {
				t.Fatal(ids, err)
			}
			settled, err := o.Settle(ctx, id, at)
			if err != nil {
				t.Fatal(err)
			}
			coins := claim.Amount / 100
			if settled.Coins != coins {
				t.Fatal(settled, claim)
			}
			if mode == "same" {
				if settled.Points != coins || settled.RefundTokenCera != 50000-claim.Amount {
					t.Fatal(settled)
				}
			} else if settled.Points != 0 || settled.RefundTokenCera != 0 {
				t.Fatal(settled)
			}
			if settled.Sender != hongbaoRequest(1, "").Owner {
				t.Fatal(settled)
			}
			again, err := o.Settle(ctx, id, at.Add(time.Hour))
			if err != nil || !again.Replayed || !reflect.DeepEqual(again.MailIDs, settled.MailIDs) {
				t.Fatal(again, err)
			}
			ids, err = o.Pending(ctx, at, 10)
			if err != nil || len(ids) != 0 {
				t.Fatal(ids, err)
			}
			if mode == "same" {
				view, err = o.View(ctx, hongbaoRequest(2, "").Owner, at.Add(24*time.Hour))
				if err != nil || len(view.Records) != 0 {
					t.Fatal(view, err)
				}
			}
		})
	}
}

func TestHongbaoContributionCapIndependentAndMailFailure(t *testing.T) {
	repos, o, now := hongbaoGroup(t)
	ctx := context.Background()
	id := hongbaoIssue(t, o, now, "cap", 103938, 5).RecordID
	for i := 2; i <= 6; i++ {
		hongbaoClaim(t, o, now, id, i)
	}
	ch, _, _ := repos.Character.Load(ctx, "1")
	ch.Stats[hongbaoContributionDay] = itemexpiration.NextDailyDeleteAt(now).Unix()
	ch.Stats[hongbaoContributionCount] = 490
	ch.Stats[contributionPrefix+string(ContributionChat)] = 1
	repos.Character.Save(ctx, ch)
	deliver := o.contract.DeliverMail
	o.contract.DeliverMail = func(ctx context.Context, tx r.Group, ch r.CharacterRecord, a HongbaoMailAward, at time.Time) ([]string, error) {
		ids, err := deliver(ctx, tx, ch, a, at)
		if err != nil {
			return ids, err
		}
		return ids, errors.New("after mail write")
	}
	before := hongbaoSnapshot(t, repos)
	if _, err := o.Settle(ctx, id, now); err == nil {
		t.Fatal("mail error ignored")
	}
	if !reflect.DeepEqual(before, hongbaoSnapshot(t, repos)) {
		t.Fatal("mail or contribution escaped rollback")
	}
	o.contract.DeliverMail = deliver
	out, err := o.Settle(ctx, id, now)
	if err != nil || out.Coins != 10 || out.Points != 10 {
		t.Fatal(out, err)
	}
	next := itemexpiration.NextDailyDeleteAt(now)
	id2 := hongbaoIssue(t, o, next, "newday", 103938, 5).RecordID
	for i := 2; i <= 6; i++ {
		hongbaoClaim(t, o, next, id2, i)
	}
	out, err = o.Settle(ctx, id2, next)
	if err != nil || out.Coins != 500 {
		t.Fatal(out, err)
	}
}

func hongbaoSnapshot(t *testing.T, repos r.Group) any {
	t.Helper()
	ctx := context.Background()
	reg, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	ch, _, _ := repos.Character.Load(ctx, "1")
	a, _, _ := repos.Account.Load(ctx, "a1")
	inv, _, _ := repos.Inventory.Load(ctx, "1")
	mail, _, _ := repos.Mailbox.Load(ctx, "1")
	return []any{reg, ch, a, inv, mail}
}

type hongbaoFaultUOW struct {
	r.CharacterSettlementUnitOfWork
	inner r.GuildSettlementUnitOfWork
	mode  string
	fail  bool
}

func (u *hongbaoFaultUOW) WithinGuildSettlement(ctx context.Context, owner r.GuildOwner, key string, fn func(r.Group) error) error {
	err := u.inner.WithinGuildSettlement(ctx, owner, key, func(tx r.Group) error {
		if u.fail && u.mode == "store" {
			tx.Settings = &hongbaoFailSettings{tx.Settings}
		}
		return fn(tx)
	})
	if err != nil {
		u.fail = false
		return err
	}
	if u.fail && u.mode == "unknown" {
		u.fail = false
		return r.ErrGuildCommitUnknown
	}
	return nil
}

type hongbaoFailSettings struct{ r.SettingsRepository }

func (s *hongbaoFailSettings) Save(ctx context.Context, record r.SettingsRecord) error {
	if err := s.SettingsRepository.Save(ctx, record); err != nil {
		return err
	}
	return errors.New("injected storage failure after staged save")
}

func TestHongbaoLateStoreAndUnknownCommitRecovery(t *testing.T) {
	for _, op := range []string{"purchase", "issue", "claim", "settle"} {
		for _, mode := range []string{"store", "unknown"} {
			t.Run(op+"/"+mode, func(t *testing.T) {
				repos, o, now := hongbaoGroup(t)
				ctx := context.Background()
				id := uint32(0)
				if op != "purchase" {
					p, err := o.Purchase(ctx, HongbaoPurchaseRequest{hongbaoRequest(1, "seed"), 103941}, now)
					if err != nil {
						t.Fatal(err)
					}
					if op != "issue" {
						issued, err := o.Issue(ctx, HongbaoIssueRequest{hongbaoRequest(1, "seed"), p.Product.ItemID, 5}, now)
						if err != nil {
							t.Fatal(err)
						}
						id = issued.RecordID
					}
				}
				if op == "settle" {
					for i := 2; i <= 6; i++ {
						hongbaoClaim(t, o, now, id, i)
					}
				}
				fault := &hongbaoFaultUOW{CharacterSettlementUnitOfWork: repos.CharacterSettlement, inner: repos.CharacterSettlement.(r.GuildSettlementUnitOfWork), mode: mode, fail: true}
				repos.CharacterSettlement = fault
				o, err := NewHongbaoOwner(repos, hongbaoFixtureContract(t))
				if err != nil {
					t.Fatal(err)
				}
				var replayed bool
				run := func() error {
					switch op {
					case "purchase":
						x, e := o.Purchase(ctx, HongbaoPurchaseRequest{hongbaoRequest(1, "fault"), 103941}, now)
						replayed = x.Replayed
						return e
					case "issue":
						x, e := o.Issue(ctx, HongbaoIssueRequest{hongbaoRequest(1, "fault"), 490003266, 5}, now)
						replayed = x.Replayed
						return e
					case "claim":
						x, e := o.Claim(ctx, HongbaoClaimRequest{hongbaoRequest(2, "fault"), id}, now)
						replayed = x.Replayed
						return e
					default:
						x, e := o.Settle(ctx, id, now)
						replayed = x.Replayed
						return e
					}
				}
				before := hongbaoSnapshot(t, repos)
				a2, _, _ := repos.Account.Load(ctx, "a2")
				if err := run(); err == nil {
					t.Fatal("injected error absent")
				}
				if mode == "store" {
					after2, _, _ := repos.Account.Load(ctx, "a2")
					if !reflect.DeepEqual(before, hongbaoSnapshot(t, repos)) || !reflect.DeepEqual(a2, after2) {
						t.Fatal("partial mutation")
					}
				}
				if err := run(); err != nil {
					t.Fatal(err)
				}
				if replayed != (mode == "unknown") {
					t.Fatal(replayed, mode)
				}
				snapshot := hongbaoSnapshot(t, repos)
				if err := run(); err != nil || !replayed {
					t.Fatal(err, replayed)
				}
				if !reflect.DeepEqual(snapshot, hongbaoSnapshot(t, repos)) {
					t.Fatal("repeat mutation")
				}
			})
		}
	}
}

func TestHongbaoExpiryClaimRaceAndListLimit(t *testing.T) {
	repos, o, now := hongbaoGroup(t)
	ctx := context.Background()
	id := hongbaoIssue(t, o, now, "expiry", 103941, 5).RecordID
	at := now.Add(24 * time.Hour)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Go(func() {
		_, err := o.Claim(ctx, HongbaoClaimRequest{hongbaoRequest(2, "late"), id}, at)
		if !errors.Is(err, ErrHongbaoUnavailable) {
			errs <- fmt.Errorf("late claim: %v", err)
		}
	})
	wg.Go(func() {
		s, err := o.Settle(ctx, id, at)
		if err != nil || s.RefundTokenCera != 5000 || s.Coins != 0 {
			errs <- fmt.Errorf("expiry settlement %+v: %v", s, err)
		}
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if _, err := o.Purchase(ctx, HongbaoPurchaseRequest{hongbaoRequest(1, "second"), 103941}, now); err != nil {
		t.Fatal(err)
	}
	reg, _ := Load(ctx, repos.Settings)
	seed := reg.Hongbao.Records[id]
	seed.Settlement = nil
	seed.SettledAt = time.Time{}
	for i := uint32(1); i <= HongbaoVisibleLimit; i++ {
		h := seed.copy()
		h.ID = i
		reg.Hongbao.Records[i] = h
	}
	reg.Hongbao.NextID = HongbaoVisibleLimit + 1
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Issue(ctx, HongbaoIssueRequest{hongbaoRequest(1, "over-limit"), 490003266, 5}, now); !errors.Is(err, ErrHongbaoListFull) {
		t.Fatal(err)
	}
	view, err := o.View(ctx, hongbaoRequest(1, "").Owner, now)
	if err != nil || len(view.Records) != 500 {
		t.Fatal(len(view.Records), err)
	}
}
