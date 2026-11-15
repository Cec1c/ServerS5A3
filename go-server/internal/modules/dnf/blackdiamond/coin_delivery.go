package blackdiamond

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// CoinsPerMail is the bounded attachment quantity passed to the current
	// mailbox writer. Pending remains durable for later batches.
	CoinsPerMail int64 = 31
	// NormalMailboxCapacity mirrors the current native normal-mail page. Saved
	// mail has a separate page and does not crowd out a new reward.
	NormalMailboxCapacity = 20
	coinTemplateRawSize   = 0x77
)

var (
	ErrCoinTemplateInvalid = errors.New("black diamond coin template is invalid")
	ErrMailboxFull         = errors.New("black diamond coin mailbox is full")
	ErrDeliveryUnavailable = errors.New("black diamond coin delivery transaction is unavailable")
	ErrDeliveryState       = errors.New("black diamond coin ledger is invalid")
)

// DeliverBlackDiamondCoins moves at most CoinsPerMail coins from the durable
// character pending ledger to one system mail. The repository's existing
// account+character+mailbox unit of work supplies the owner locks and commits
// the character delivered_total update and mailbox write atomically. Delivery
// deliberately does not consult current Black Diamond membership: coins earned
// before expiry remain deliverable.
//
// A successful call is not keyed by an external request receipt. A retry sees
// the committed delivered_total; a pending balance of at most one mail becomes
// a no-op, while a larger balance may legitimately produce the next bounded
// batch. The caller should retain its request receipt when it needs exactly-once
// command semantics.
func DeliverBlackDiamondCoins(
	ctx context.Context,
	repos dnfrepo.Group,
	accountID, characterID string,
	now time.Time,
	coin dnfrepo.ItemStack,
) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	accountID = strings.TrimSpace(accountID)
	characterID = strings.TrimSpace(characterID)
	if accountID == "" || characterID == "" || now.IsZero() {
		return false, fmt.Errorf("%w: owner or time is missing", ErrDeliveryUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	attachmentTemplate, err := normalizeCoinTemplate(coin, now.UTC())
	if err != nil {
		return false, err
	}
	if repos.Mailbox == nil {
		return false, fmt.Errorf("%w: mailbox repository is missing", ErrDeliveryUnavailable)
	}
	uow, ok := repos.AccountAssets.(dnfrepo.AccountCharacterMailboxAssetUnitOfWork)
	if !ok {
		return false, fmt.Errorf("%w: account-character-mailbox unit is missing", ErrDeliveryUnavailable)
	}

	delivered := false
	err = uow.WithinAccountCharacterMailboxAssets(
		ctx,
		accountID,
		characterID,
		func(
			_ dnfrepo.AccountInventoryRepository,
			characters dnfrepo.CharacterRepository,
			_ dnfrepo.InventoryRepository,
			_ dnfrepo.EquipmentRepository,
			mailboxes dnfrepo.MailboxRepository,
		) error {
			character, found, loadErr := characters.Load(ctx, characterID)
			if loadErr != nil {
				return loadErr
			}
			if !found || character.CharacterID != characterID || character.AccountID != accountID {
				return dnfrepo.ErrAccountCharacterMailboxOwnership
			}
			character = dnfrepo.CloneCharacter(character)
			credited, deliveredTotal, pending, stateErr := coinLedger(character)
			if stateErr != nil {
				return stateErr
			}
			if pending == 0 {
				return nil
			}

			mailbox, mailboxFound, loadErr := mailboxes.Load(ctx, characterID)
			if loadErr != nil {
				return loadErr
			}
			if mailboxFound && blackDiamondNormalMailCount(mailbox, now.UTC()) >= NormalMailboxCapacity {
				return fmt.Errorf("%w: normal=%d", ErrMailboxFull, NormalMailboxCapacity)
			}

			count := pending
			if count > CoinsPerMail {
				count = CoinsPerMail
			}
			attachment := attachmentTemplate
			attachment.Count = count
			if count != 1 {
				// The PVF template describes one coin. A batch is projected by the
				// mailbox writer from ItemID/Count and must not retain a stale slot row.
				attachment.RawEntry = nil
			}
			completionKey := fmt.Sprintf(
				"black_diamond_fatigue:v1:%s:%d-%d",
				characterID,
				deliveredTotal,
				deliveredTotal+count,
			)
			for _, existing := range mailbox.Mails {
				if existing.Metadata["completion_key"] == completionKey {
					return fmt.Errorf("%w: completion key already has mail", ErrDeliveryState)
				}
			}
			if _, appendErr := dnfrepo.AppendSystemMail(ctx, mailboxes, dnfrepo.SystemMailDelivery{
				RecipientCharacterID: characterID,
				Title:                "黑钻疲劳奖励",
				Body:                 fmt.Sprintf("疲劳累计消耗达到%d点，赠予黑钻硬币%d枚。", premium.BlackDiamondFatigueThreshold, count),
				Source:               "黑钻疲劳奖励",
				CompletionKey:        completionKey,
				Attachments:          []dnfrepo.MailAttachment{attachment},
				CreatedAt:            now.UTC(),
			}); appendErr != nil {
				return appendErr
			}

			if character.Stats == nil {
				character.Stats = make(map[string]int64, 4)
			}
			character.Stats[premium.BlackDiamondFatigueCreditedTotalKey] = credited
			character.Stats[premium.BlackDiamondFatigueDeliveredTotalKey] = deliveredTotal + count
			if saveErr := dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats); saveErr != nil {
				return saveErr
			}
			delivered = true
			return nil
		},
	)
	if err != nil {
		return false, err
	}
	return delivered, nil
}

func normalizeCoinTemplate(coin dnfrepo.ItemStack, now time.Time) (dnfrepo.MailAttachment, error) {
	if coin.ItemID != premium.BlackDiamondCoinItemID || coin.Count != 1 || coin.Bind {
		return dnfrepo.MailAttachment{}, fmt.Errorf(
			"%w: item=%d count=%d bind=%t",
			ErrCoinTemplateInvalid,
			coin.ItemID,
			coin.Count,
			coin.Bind,
		)
	}
	if !coin.ExpireAt.IsZero() && !coin.ExpireAt.After(now) {
		return dnfrepo.MailAttachment{}, fmt.Errorf("%w: expiration is not in the future", ErrCoinTemplateInvalid)
	}
	if len(coin.RawEntry) != 0 {
		if len(coin.RawEntry) != coinTemplateRawSize ||
			int64(binary.LittleEndian.Uint32(coin.RawEntry[2:6])) != premium.BlackDiamondCoinItemID ||
			binary.LittleEndian.Uint32(coin.RawEntry[6:10]) != 1 ||
			coin.RawEntry[0x0d] != 0 {
			return dnfrepo.MailAttachment{}, fmt.Errorf("%w: raw instance is not an unbound single coin", ErrCoinTemplateInvalid)
		}
	}

	extra := make(map[string]string, len(coin.Extra)+3)
	for key, value := range coin.Extra {
		if !blackDiamondCoinExtraKeyAllowed(key) {
			return dnfrepo.MailAttachment{}, fmt.Errorf("%w: unrelated extra key %q", ErrCoinTemplateInvalid, key)
		}
		value = strings.TrimSpace(value)
		switch key {
		case "item_kind":
			if !strings.EqualFold(value, "stackable") {
				return dnfrepo.MailAttachment{}, fmt.Errorf("%w: item_kind=%q", ErrCoinTemplateInvalid, value)
			}
		case "stackable_type":
			if value != "[material]" {
				return dnfrepo.MailAttachment{}, fmt.Errorf("%w: stackable_type=%q", ErrCoinTemplateInvalid, value)
			}
		case "pvf_path":
			normalized := strings.ToLower(strings.ReplaceAll(value, "\\", "/"))
			if value == "" || !strings.HasPrefix(normalized, "stackable/") || strings.Contains(normalized, "..") {
				return dnfrepo.MailAttachment{}, fmt.Errorf("%w: pvf_path=%q", ErrCoinTemplateInvalid, value)
			}
		}
		extra[key] = value
	}
	// Keep the attachment's item family explicit even for a minimal verified
	// template. The delivery source is server-owned and cannot be overridden by
	// the caller's metadata.
	extra["source"] = "black_diamond_fatigue_reward"
	extra["item_kind"] = "stackable"
	extra["stackable_type"] = "[material]"

	return dnfrepo.MailAttachment{
		ItemID:   premium.BlackDiamondCoinItemID,
		Count:    1,
		ExpireAt: coin.ExpireAt,
		RawEntry: append([]byte(nil), coin.RawEntry...),
		Extra:    extra,
	}, nil
}

func blackDiamondCoinExtraKeyAllowed(key string) bool {
	switch key {
	case "source", "item_kind", "pvf_path", "stackable_type", "stack_limit", "expire_time", "expire_unix", "daily_delete_at":
		return true
	default:
		return false
	}
}

func coinLedger(character dnfrepo.CharacterRecord) (credited, delivered, pending int64, err error) {
	for _, key := range []string{
		premium.BlackDiamondFatigueDayKey,
		premium.BlackDiamondFatigueSpentKey,
		premium.BlackDiamondFatigueCreditedTotalKey,
		premium.BlackDiamondFatigueDeliveredTotalKey,
	} {
		if value, ok := character.Stats[key]; ok && value < 0 {
			return 0, 0, 0, fmt.Errorf("%w: %s=%d", ErrDeliveryState, key, value)
		}
	}
	credited = character.Stats[premium.BlackDiamondFatigueCreditedTotalKey]
	delivered = character.Stats[premium.BlackDiamondFatigueDeliveredTotalKey]
	if delivered > credited {
		return 0, 0, 0, fmt.Errorf("%w: delivered=%d credited=%d", ErrDeliveryState, delivered, credited)
	}
	return credited, delivered, credited - delivered, nil
}

func blackDiamondNormalMailCount(mailbox dnfrepo.MailboxRecord, now time.Time) int {
	count := 0
	for _, mail := range mailbox.Mails {
		if mail.Deleted || strings.EqualFold(strings.TrimSpace(mail.Metadata["mailbox_saved"]), "true") {
			continue
		}
		if !mail.ExpireAt.IsZero() && !mail.ExpireAt.After(now) {
			continue
		}
		count++
	}
	return count
}
