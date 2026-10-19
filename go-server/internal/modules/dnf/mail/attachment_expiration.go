package mail

import (
	"fmt"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Player mail and auction returns transfer existing items. Their envelope's
// creation time is not the acquisition time of an attachment.
func MailCreationAnchorsAttachment(record dnfrepo.MailRecord) bool {
	sender := strings.TrimSpace(record.SenderCharacterID)
	if sender != "" && sender != "0" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(record.Metadata["source"])) {
	case "op94", "op315", "auction_purchase_delivery", "auction_listing_return", "auction_listing_expired":
		return false
	}
	return true
}

// A claim transfers the original attachment; it does not create a fresh
// lifetime. Missing provenance aborts the enclosing asset transaction.
func mailboxAttachmentWithOriginalExpiry(attachment dnfrepo.MailAttachment, resolution alignedcmd.MailboxItemResolution, createdAt time.Time) (dnfrepo.MailAttachment, error) {
	if resolution.UsablePeriodDays <= 0 {
		return attachment, nil
	}
	if resolution.OriginalExpiry != nil {
		if original := resolution.OriginalExpiry(attachment); !original.IsZero() {
			attachment.ExpireAt = original
			return attachment, nil
		}
	}
	if !attachment.ExpireAt.IsZero() {
		return attachment, nil
	}
	if createdAt.IsZero() {
		return attachment, fmt.Errorf("%w: relative attachment missing original expiry and mail creation time", ErrMailboxAssetMismatch)
	}
	anchor := createdAt.Unix()
	if anchor <= 0 || anchor > math.MaxUint32 || resolution.UsablePeriodDays > (math.MaxUint32-anchor)/86400 {
		return attachment, fmt.Errorf("%w: relative attachment expiry exceeds item range", ErrMailboxAssetMismatch)
	}
	attachment.ExpireAt = time.Unix(anchor+resolution.UsablePeriodDays*86400, 0).UTC()
	return attachment, nil
}
