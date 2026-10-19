package mail

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 邮件领取的期限锚点是“原实例”，不是本次点击领取的时间。
//
// 真实 PVF 的相对 [usable period] 消耗品（活动 PVF 10304122，七天，stack limit 1）
// 不应在领取时重新起算；附件自己带 ExpireAt 时它优先，否则用邮件 CreatedAt
// 加 PVF 天数。两样都没有时必须 fail closed，整批资产保持可领取。
const (
	usablePeriodClaimItemID int64 = 10304122
	usablePeriodClaimDays   int64 = 7
)

// usablePeriodClaimFixture 复用本包既有的 cubeWarehouseMailFixtures：它已经建好
// 角色 77、一个可领取的普通附件邮件和内存事务。这里只补上本组用例关心的
// CreatedAt / 附件 ExpireAt，避免再养一套 mailbox fake。
func usablePeriodClaimFixture(t *testing.T, createdAt, attachmentExpireAt time.Time) (context.Context, dnfrepo.Group, uint32) {
	t.Helper()
	ctx, repos, mailID := cubeWarehouseMailFixtures(t, usablePeriodClaimItemID, 1)
	box, found, err := repos.Mailbox.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("load mailbox found=%v err=%v", found, err)
	}
	key := strconv.FormatUint(uint64(mailID), 10)
	mail, ok := box.Mails[key]
	if !ok || len(mail.Attachments) != 1 {
		t.Fatalf("fixture mail %q = %+v, want one attachment", key, mail)
	}
	mail.CreatedAt = createdAt
	mail.Attachments[0].ExpireAt = attachmentExpireAt
	box.Mails[key] = mail
	if err := repos.Mailbox.Save(ctx, box); err != nil {
		t.Fatalf("save fixture mailbox: %v", err)
	}
	return ctx, repos, mailID
}

// usablePeriodClaimResolver 在现成的 mailboxClaimTestResolver 上补齐相对期限证据。
// projectedExpire 模拟旧 dnfbridge 在领取时刻算出的 now+days 投影；领域规则必须
// 用邮件 CreatedAt 重算，不能把这个投影当成实例期限。
func usablePeriodClaimResolver(days int64, projectedExpire time.Time) alignedcmd.MailboxItemResolver {
	base := mailboxClaimTestResolver(65, 120, 1)
	return func(itemID uint32) (alignedcmd.MailboxItemResolution, error) {
		resolution, err := base(itemID)
		if err != nil {
			return resolution, err
		}
		resolution.UsablePeriodDays = days
		resolution.StackLimit = 1
		resolution.ExpireAt = projectedExpire
		resolution.DefaultExtra = map[string]string{
			"item_kind":          "stackable",
			"usable_period_days": strconv.FormatInt(days, 10),
			"expiration_source":  "runtime_pvf_usable_period_grant",
		}
		if !projectedExpire.IsZero() {
			projected := strconv.FormatInt(projectedExpire.Unix(), 10)
			resolution.DefaultExtra["expire_time"] = projected
			resolution.DefaultExtra["expire_unix"] = projected
		}
		return resolution, nil
	}
}

func usablePeriodClaimOwner(repos dnfrepo.Group, resolver alignedcmd.MailboxItemResolver, now time.Time) Owner {
	owner := NewOwner(repos, resolver)
	owner.now = func() time.Time { return now }
	return owner
}

func claimedUsablePeriodStack(t *testing.T, ctx context.Context, repos dnfrepo.Group) dnfrepo.ItemStack {
	t.Helper()
	inventory, found, err := repos.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	if !found {
		t.Fatalf("inventory missing after a successful claim")
	}
	for _, stack := range inventory.Slots {
		if stack.ItemID == usablePeriodClaimItemID {
			return stack
		}
	}
	t.Fatalf("claimed item %d missing from inventory: %+v", usablePeriodClaimItemID, inventory.Slots)
	return dnfrepo.ItemStack{}
}

func assertClaimedUsablePeriodExpiry(t *testing.T, stack dnfrepo.ItemStack, want time.Time, days int64) {
	t.Helper()
	if !stack.ExpireAt.Equal(want) {
		t.Fatalf("claimed expire_at = %s, want %s", stack.ExpireAt.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	wantUnix := strconv.FormatInt(want.Unix(), 10)
	for _, key := range []string{"expire_time", "expire_unix"} {
		if value, ok := stack.Extra[key]; ok && value != wantUnix {
			t.Errorf("claimed Extra[%q] = %q, want %q (durable expire_at=%s)", key, value, wantUnix, stack.ExpireAt.Format(time.RFC3339))
		}
	}
	if stack.Extra["expiration_source"] == "runtime_pvf_usable_period_grant" {
		for _, key := range []string{"expire_time", "expire_unix"} {
			if _, ok := stack.Extra[key]; !ok {
				t.Errorf("relative-grant stack is missing %s: %+v", key, stack.Extra)
			}
		}
	}
	if got := stack.Extra["usable_period_days"]; got != "" && got != strconv.FormatInt(days, 10) {
		t.Errorf("claimed usable_period_days = %q, want %d", got, days)
	}
}

func assertNoUsablePeriodItem(t *testing.T, ctx context.Context, repos dnfrepo.Group) {
	t.Helper()
	inventory, found, err := repos.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	if !found {
		return
	}
	for _, stack := range inventory.Slots {
		if stack.ItemID == usablePeriodClaimItemID {
			t.Fatalf("failed claim still delivered item %d: %+v", usablePeriodClaimItemID, stack)
		}
	}
}

func assertUsablePeriodMailIntact(t *testing.T, ctx context.Context, repos dnfrepo.Group, mailID uint32) {
	t.Helper()
	box, found, err := repos.Mailbox.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("load mailbox found=%v err=%v", found, err)
	}
	mail, ok := box.Mails[strconv.FormatUint(uint64(mailID), 10)]
	if !ok {
		t.Fatalf("mail %d disappeared after a failed claim", mailID)
	}
	if mail.Claimed || mail.Deleted || len(mail.Attachments) != 1 {
		t.Fatalf("failed claim mutated the letter: %+v", mail)
	}
	if !mail.Attachments[0].ExpireAt.IsZero() {
		t.Fatalf("failed claim persisted a derived attachment expiry: %+v", mail.Attachments[0])
	}
}

// 相对期限必须从邮件 CreatedAt 起算。resolver 同时给出旧版的 now+days 投影，
// 用来钉住“领取时刻不能成为新锚点”。
func TestMailboxClaimUsablePeriodAnchorsOnMailCreatedAt(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	createdAt := now.Add(-5 * 24 * time.Hour)
	projectedAtClaim := now.Add(time.Duration(usablePeriodClaimDays) * 24 * time.Hour)
	ctx, repos, mailID := usablePeriodClaimFixture(t, createdAt, time.Time{})
	owner := usablePeriodClaimOwner(repos, usablePeriodClaimResolver(usablePeriodClaimDays, projectedAtClaim), now)

	result, err := owner.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{mailID}})
	if err != nil {
		t.Fatalf("claim relative-period attachment: %v", err)
	}
	if len(result.MailIDs) != 1 || result.MailIDs[0] != mailID {
		t.Fatalf("claimed mail ids = %v, want [%d]", result.MailIDs, mailID)
	}
	stack := claimedUsablePeriodStack(t, ctx, repos)
	if stack.Count != 1 {
		t.Fatalf("claimed count = %d, want 1", stack.Count)
	}
	want := createdAt.Add(time.Duration(usablePeriodClaimDays) * 24 * time.Hour)
	assertClaimedUsablePeriodExpiry(t, stack, want, usablePeriodClaimDays)
	if stack.ExpireAt.Equal(projectedAtClaim) {
		t.Fatalf("claim used the fresh grant projection %s instead of CreatedAt+days %s", projectedAtClaim, want)
	}

	box, found, err := repos.Mailbox.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("load mailbox found=%v err=%v", found, err)
	}
	settled := box.Mails[strconv.FormatUint(uint64(mailID), 10)]
	if !settled.Claimed || !settled.Deleted || len(settled.Attachments) != 0 {
		t.Fatalf("claimed letter = %+v, want settled with no claimable projection", settled)
	}
}

// 附件自己的 ExpireAt 优先。即使它已经过去，也按原领域行为领取为过期实例；
// CreatedAt+days 不能把它续成新期限，落地的 0x77 投影也要与实例期限一致。
func TestMailboxClaimExplicitAttachmentExpiryWinsWithoutRenewal(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	createdAt := now.Add(-2 * 24 * time.Hour)
	explicitExpire := now.Add(-72 * time.Hour)
	ctx, repos, mailID := usablePeriodClaimFixture(t, createdAt, explicitExpire)
	owner := usablePeriodClaimOwner(repos, usablePeriodClaimResolver(usablePeriodClaimDays, now.Add(time.Duration(usablePeriodClaimDays)*24*time.Hour)), now)

	if _, err := owner.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{mailID}}); err != nil {
		t.Fatalf("claim explicit-expiry attachment: %v", err)
	}
	stack := claimedUsablePeriodStack(t, ctx, repos)
	if !stack.ExpireAt.Equal(explicitExpire) {
		t.Fatalf("claimed expire_at = %s, want the original attachment expiry %s", stack.ExpireAt.Format(time.RFC3339), explicitExpire.Format(time.RFC3339))
	}
	if renewed := createdAt.Add(time.Duration(usablePeriodClaimDays) * 24 * time.Hour); stack.ExpireAt.Equal(renewed) {
		t.Fatalf("explicit expired attachment was renewed to %s", renewed)
	}
	assertClaimedUsablePeriodExpiry(t, stack, explicitExpire, usablePeriodClaimDays)
}

// 相对期限附件既没有原 ExpireAt，也没有邮件 CreatedAt：领取必须失败，
// 信件和附件保持可领取，背包不得出现半件资产。
func TestMailboxClaimUsablePeriodWithoutMailCreatedAtKeepsAssets(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	ctx, repos, mailID := usablePeriodClaimFixture(t, time.Time{}, time.Time{})
	owner := usablePeriodClaimOwner(repos, usablePeriodClaimResolver(usablePeriodClaimDays, time.Time{}), now)

	_, err := owner.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{mailID}})
	if !errors.Is(err, ErrMailboxAssetMismatch) {
		t.Fatalf("claim without CreatedAt error = %v, want %v", err, ErrMailboxAssetMismatch)
	}
	assertNoUsablePeriodItem(t, ctx, repos)
	assertUsablePeriodMailIntact(t, ctx, repos, mailID)
}

var errInjectedUsablePeriodCommit = errors.New("injected usable-period mailbox asset commit failure")

type failingUsablePeriodMailboxAssets struct {
	dnfrepo.MailboxAssetUnitOfWork
}

func (u failingUsablePeriodMailboxAssets) WithinMailboxAssets(
	ctx context.Context,
	assetCharacterID string,
	mailboxCharacterID string,
	apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.MailboxRepository) error,
) error {
	return u.MailboxAssetUnitOfWork.WithinMailboxAssets(ctx, assetCharacterID, mailboxCharacterID, func(
		characters dnfrepo.CharacterRepository,
		inventories dnfrepo.InventoryRepository,
		mailboxes dnfrepo.MailboxRepository,
	) error {
		if err := apply(characters, inventories, mailboxes); err != nil {
			return err
		}
		return errInjectedUsablePeriodCommit
	})
}

// UoW 在提交边界失败时，刚算出的附件期限/已放入事务副本的资产都不能落库。
func TestMailboxClaimUsablePeriodRollsBackOnUnitOfWorkFailure(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	createdAt := now.Add(-5 * 24 * time.Hour)
	ctx, repos, mailID := usablePeriodClaimFixture(t, createdAt, time.Time{})
	broken := repos
	broken.MailboxAssets = failingUsablePeriodMailboxAssets{MailboxAssetUnitOfWork: repos.MailboxAssets}
	owner := usablePeriodClaimOwner(broken, usablePeriodClaimResolver(usablePeriodClaimDays, now.Add(time.Duration(usablePeriodClaimDays)*24*time.Hour)), now)

	_, err := owner.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{mailID}})
	if !errors.Is(err, errInjectedUsablePeriodCommit) {
		t.Fatalf("claim error = %v, want the injected UoW failure", err)
	}
	assertNoUsablePeriodItem(t, ctx, repos)
	assertUsablePeriodMailIntact(t, ctx, repos, mailID)
}

// 重复/并发领取只允许发一次货。第一次成功后的重放可以是幂等成功或
// ErrMailboxAlreadyClaimed，但绝不能把同一附件再放进背包。
func TestMailboxClaimUsablePeriodRepeatedClaimDeliversOnce(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	createdAt := now.Add(-5 * 24 * time.Hour)
	ctx, repos, mailID := usablePeriodClaimFixture(t, createdAt, time.Time{})
	owner := usablePeriodClaimOwner(repos, usablePeriodClaimResolver(usablePeriodClaimDays, now.Add(time.Duration(usablePeriodClaimDays)*24*time.Hour)), now)

	if _, err := owner.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{mailID}}); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := owner.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{mailID}}); err != nil && !errors.Is(err, ErrMailboxAlreadyClaimed) {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("repeat claim: %v", err)
	}

	inventory, found, err := repos.Inventory.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("load inventory found=%v err=%v", found, err)
	}
	var delivered int64
	for _, stack := range inventory.Slots {
		if stack.ItemID == usablePeriodClaimItemID {
			delivered += stack.Count
		}
	}
	if delivered != 1 {
		t.Fatalf("delivered item count = %d, want 1 after repeated claims", delivered)
	}
	box, found, err := repos.Mailbox.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("load mailbox found=%v err=%v", found, err)
	}
	settled := box.Mails[strconv.FormatUint(uint64(mailID), 10)]
	if !settled.Claimed || !settled.Deleted || len(settled.Attachments) != 0 {
		t.Fatalf("repeated claim left a claimable projection: %+v", settled)
	}
}
