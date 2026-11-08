package raid

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// EntryDebit is frozen before admission and persisted with the material debit.
// RefundItem may differ for a PVF-configured retry ticket. It is never supplied
// by a client refund request, inferred from current inventory, or rerolled.
type EntryDebit struct {
	InstanceID   string
	Mode         byte
	ItemID       uint32
	Count        int64
	RefundItemID uint32
	RefundCount  int64
	PaidAt       time.Time
}

func entryDebitKey(d EntryDebit) (string, error) {
	id, err := hex.DecodeString(d.InstanceID)
	if err != nil || len(id) != 16 || d.InstanceID != hex.EncodeToString(id) || d.Mode < 1 || d.Mode > 3 || d.ItemID == 0 || d.Count <= 0 || d.Count > math.MaxUint16 || d.RefundItemID == 0 || d.RefundCount <= 0 || d.RefundCount > math.MaxUint16 || d.PaidAt.Unix() <= 0 {
		return "", ErrRewardReceipt
	}
	return fmt.Sprintf("raid_entry_v1_%d_%s", d.Mode, d.InstanceID), nil
}

// RecordEntryDebit must run in the same locked transaction as take-entry-cost.
// An existing identity is a conflict, so an uncertain admission is not charged
// a second time. Keeping the receipt does not reset daily/weekly quotas.
func RecordEntryDebit(character *repo.CharacterRecord, d EntryDebit) error {
	key, err := entryDebitKey(d)
	if err != nil || character == nil || strings.TrimSpace(character.CharacterID) == "" {
		return ErrRewardReceipt
	}
	if character.Stats == nil {
		character.Stats = map[string]int64{}
	}
	for _, suffix := range []string{"_paid", "_item", "_count", "_return_item", "_return_count", "_failed", "_mail"} {
		if _, exists := character.Stats[key+suffix]; exists {
			return ErrRewardReceipt
		}
	}
	character.Stats[key+"_paid"] = d.PaidAt.Unix()
	character.Stats[key+"_item"] = int64(d.ItemID)
	character.Stats[key+"_count"] = d.Count
	character.Stats[key+"_return_item"] = int64(d.RefundItemID)
	character.Stats[key+"_return_count"] = d.RefundCount
	return nil
}

type EntryRefundStatus struct {
	Paid     bool
	Refunded bool
	FailedAt time.Time
	MailID   string
}

// QueryEntryRefund is read-only and checks the exact paid instance/policy.
// Missing old receipts never authorize a speculative or historical refund.
func QueryEntryRefund(character repo.CharacterRecord, d EntryDebit) (EntryRefundStatus, error) {
	key, err := entryDebitKey(d)
	if err != nil {
		return EntryRefundStatus{}, err
	}
	s := character.Stats
	if s[key+"_paid"] != d.PaidAt.Unix() || s[key+"_item"] != int64(d.ItemID) || s[key+"_count"] != d.Count || s[key+"_return_item"] != int64(d.RefundItemID) || s[key+"_return_count"] != d.RefundCount {
		return EntryRefundStatus{}, ErrRewardReceipt
	}
	status := EntryRefundStatus{Paid: true}
	failed, hasFailure := s[key+"_failed"]
	mailID, hasMail := s[key+"_mail"]
	if hasFailure != hasMail || hasFailure && (failed < d.PaidAt.Unix() || mailID <= 0 || mailID > math.MaxUint32) {
		return EntryRefundStatus{}, ErrRewardReceipt
	}
	if hasMail {
		status.Refunded = true
		status.FailedAt = time.Unix(failed, 0).UTC()
		status.MailID = strconv.FormatInt(mailID, 10)
	}
	return status, nil
}

type EntryRefundCommand struct {
	Debit       EntryDebit
	AccountID   string
	CharacterID string
	FailedAt    time.Time
	Attachments []repo.MailAttachment
}

// RefundEntryByMail receives an authenticated server-owned failed instance.
// Mail and its receipt commit together. A lost acknowledgement, retry, later
// query, full bag, or deleted/claimed mail cannot create a second ticket.
func RefundEntryByMail(ctx context.Context, group repo.Group, cmd EntryRefundCommand) (EntryRefundStatus, error) {
	key, err := entryDebitKey(cmd.Debit)
	if err != nil || cmd.AccountID == "" || cmd.CharacterID == "" || strings.TrimSpace(cmd.AccountID) != cmd.AccountID || strings.TrimSpace(cmd.CharacterID) != cmd.CharacterID || cmd.FailedAt.Before(cmd.Debit.PaidAt) || len(cmd.Attachments) == 0 {
		return EntryRefundStatus{}, ErrRewardReceipt
	}
	var total int64
	for _, item := range cmd.Attachments {
		if item.ItemID != int64(cmd.Debit.RefundItemID) || item.Count <= 0 || item.Count > cmd.Debit.RefundCount-total {
			return EntryRefundStatus{}, ErrRewardReceipt
		}
		total += item.Count
	}
	if total != cmd.Debit.RefundCount {
		return EntryRefundStatus{}, ErrRewardReceipt
	}
	u, ok := group.AccountAssets.(repo.AccountCharacterMailboxAssetUnitOfWork)
	if !ok {
		return EntryRefundStatus{}, ErrRewardOwnerUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result EntryRefundStatus
	err = u.WithinAccountCharacterMailboxAssets(ctx, cmd.AccountID, cmd.CharacterID, func(_ repo.AccountInventoryRepository, chars repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository, mailboxes repo.MailboxRepository) error {
		c, found, err := chars.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || c.CharacterID != cmd.CharacterID || c.AccountID != cmd.AccountID {
			return ErrRewardOwnership
		}
		status, err := QueryEntryRefund(c, cmd.Debit)
		if err != nil {
			return err
		}
		if status.Refunded {
			if status.FailedAt.Unix() != cmd.FailedAt.Unix() {
				return ErrRewardReceipt
			}
			result = status
			return nil
		}
		mailID, err := repo.AppendSystemMail(ctx, mailboxes, repo.SystemMailDelivery{RecipientCharacterID: cmd.CharacterID, Title: "攻坚失败入场券返还", Body: "本次攻坚失败，已按开战时记录的规则返还入场券。重复查询不会重复发放。", Source: "raid_entry_refund", CompletionKey: key, CreatedAt: cmd.FailedAt, Attachments: cmd.Attachments})
		if err != nil {
			return err
		}
		n, err := strconv.ParseInt(mailID, 10, 64)
		if err != nil || n <= 0 || n > math.MaxUint32 {
			return ErrRewardReceipt
		}
		c = repo.CloneCharacter(c)
		c.Stats[key+"_failed"] = cmd.FailedAt.Unix()
		c.Stats[key+"_mail"] = n
		c.UpdatedAt = cmd.FailedAt.UTC()
		if err = repo.SaveCharacterFields(ctx, chars, c, repo.CharacterFieldStats); err != nil {
			return err
		}
		result = EntryRefundStatus{Paid: true, Refunded: true, FailedAt: time.Unix(cmd.FailedAt.Unix(), 0).UTC(), MailID: mailID}
		return nil
	})
	if err != nil {
		return EntryRefundStatus{}, err
	}
	return result, nil
}
