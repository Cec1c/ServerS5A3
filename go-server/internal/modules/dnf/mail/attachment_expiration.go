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

// mailCreationProvesNewAttachment requires evidence from AppendSystemMail,
// as well as a named source and no transfer sender/source. The older relative
// expiry policy deliberately keeps its separate compatibility gate.
func mailCreationProvesNewAttachment(record dnfrepo.MailRecord) bool {
	return MailCreationAnchorsAttachment(record) &&
		strings.TrimSpace(record.SenderCharacterID) == "0" &&
		strings.TrimSpace(record.Metadata["source"]) != "" &&
		record.Metadata["system_mail"] == "true" &&
		record.Metadata["attachment_grant_anchor"] == dnfrepo.SystemMailCreationAnchor
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

// mailboxAttachmentWithClaimProjection applies evidence computed for this
// attachment. The enclosing owner transaction still owns the durable write, so
// a conflict aborts the claim instead of silently mixing two deadlines.
func mailboxAttachmentWithClaimProjection(
	attachment dnfrepo.MailAttachment,
	projection alignedcmd.MailboxAttachmentProjection,
) (dnfrepo.MailAttachment, error) {
	if !projection.ExpireAt.IsZero() {
		if !attachment.ExpireAt.IsZero() && !attachment.ExpireAt.Equal(projection.ExpireAt) {
			return attachment, fmt.Errorf("%w: attachment expiry conflicts with per-mail projection", ErrMailboxAssetMismatch)
		}
		attachment.ExpireAt = projection.ExpireAt.UTC()
	}
	if len(projection.Extra) == 0 {
		return attachment, nil
	}
	// A candidate may be discarded for capacity or a later transaction error.
	// Never mutate the mailbox's map through the attachment value copy.
	attachment.Extra = cloneStringMap(attachment.Extra)
	if attachment.Extra == nil {
		attachment.Extra = make(map[string]string, len(projection.Extra))
	}
	for key, value := range projection.Extra {
		if value == "" {
			continue
		}
		if existing := strings.TrimSpace(attachment.Extra[key]); existing != "" && existing != value {
			return attachment, fmt.Errorf("%w: attachment extra %s conflicts with per-mail projection", ErrMailboxAssetMismatch, key)
		}
		attachment.Extra[key] = value
	}
	return attachment, nil
}
