package mail

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestMailboxRelativeExpiryTransferDoesNotUseEnvelopeClock(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	cases := []struct{ name, sender, source string }{
		{"player", "42", "op94"},
		{"legacy_player", "", "op315"},
		{"auction_purchase", "0", "auction_purchase_delivery"},
		{"auction_return", "0", "auction_listing_return"},
		{"auction_expired", "0", "auction_listing_expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, known := range []bool{false, true} {
				name := "missing_instance"
				if known {
					name = "original_instance"
				}
				t.Run(name, func(t *testing.T) {
					expiry := time.Time{}
					if known {
						expiry = now.Add(-time.Hour)
					}
					ctx, repos, mailID := usablePeriodClaimFixture(t, now.Add(-time.Hour), expiry)
					box, _, err := repos.Mailbox.Load(ctx, "77")
					if err != nil {
						t.Fatal(err)
					}
					key := strconv.FormatUint(uint64(mailID), 10)
					letter := box.Mails[key]
					letter.SenderCharacterID = tc.sender
					letter.Metadata = map[string]string{"source": tc.source}
					box.Mails[key] = letter
					if err := repos.Mailbox.Save(ctx, box); err != nil {
						t.Fatal(err)
					}
					owner := usablePeriodClaimOwner(repos, usablePeriodClaimResolver(7, now.Add(7*24*time.Hour)), now)
					_, err = owner.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{mailID}})
					if !known {
						if !errors.Is(err, ErrMailboxAssetMismatch) {
							t.Fatalf("transfer without original deadline: %v", err)
						}
						assertNoUsablePeriodItem(t, ctx, repos)
						assertUsablePeriodMailIntact(t, ctx, repos, mailID)
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					assertClaimedUsablePeriodExpiry(t, claimedUsablePeriodStack(t, ctx, repos), expiry, 7)
				})
			}
		})
	}
}

func TestMailboxRelativeExpiryOverflowPreservesAssets(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	ctx, repos, mailID := usablePeriodClaimFixture(t, now.Add(-time.Hour), time.Time{})
	owner := usablePeriodClaimOwner(repos, usablePeriodClaimResolver(1<<62, now.Add(7*24*time.Hour)), now)
	_, err := owner.Claim(ctx, "77", ExtractRequest{MailIDs: []uint32{mailID}})
	if !errors.Is(err, ErrMailboxAssetMismatch) {
		t.Fatalf("overflow: %v", err)
	}
	assertNoUsablePeriodItem(t, ctx, repos)
	assertUsablePeriodMailIntact(t, ctx, repos, mailID)
}
