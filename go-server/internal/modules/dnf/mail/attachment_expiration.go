package mail

import (
	"fmt"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/itemexpiration"
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
	// ★ 相对 [usable period] 的附件到期时间吸附到"服务日 06:00（北京时间）"（2026-11-07）。
	//
	// 与 dnfbridge 的 currentPVFItemDefinitionWithUsablePeriodAt 同一口径：PVF 相对天数
	// 落到秒会让同一天领取的同一限期道具得到互不相同的绝对时间，而背包堆叠合并要求
	// 过期时间整数秒相等 ⇒ 领出来就是散格。邮件这条路径原来自己算 CreatedAt+N×24h，
	// 绕过了那个通用函数，所以必须在这里补同一次吸附。
	//
	// 绝对 [expiration date] 不走这里（resolution.OriginalExpiry / attachment.ExpireAt
	// 在上面已经 return），[daily delete item] 也不会设置 UsablePeriodDays。
	expireAt := itemexpiration.DailyDeleteBoundaryAtOrAfter(time.Unix(anchor+resolution.UsablePeriodDays*86400, 0).UTC())
	if expireAt.Unix() > math.MaxUint32 {
		return attachment, fmt.Errorf("%w: relative attachment expiry exceeds item range", ErrMailboxAssetMismatch)
	}
	attachment.ExpireAt = expireAt
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
