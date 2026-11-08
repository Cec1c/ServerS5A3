package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Caller holds raidMu. Freeze the paid roster, not the remaining online seats.
// The same instance key covers both phases and survives lobby removal.
func (s *Service) freezeCurrentRaidFailureRefundLocked(state runtimeRaidState, now time.Time) error {
	b := state.Battle
	if b == nil || !b.TimedOut || b.PhaseCompleted {
		return nil
	}
	if b.EntryDebit == nil {
		// Historical in-process FiendWar owners retain their original receipt
		// path. Missing old Anton/Luke debit evidence must never mint tickets.
		if b.FiendWar != nil {
			return s.freezeCurrentFiendWarRetryLocked(state, now)
		}
		return nil
	}
	d := *b.EntryDebit
	if d.InstanceID != b.InstanceID || d.Mode != b.Mode || d.Mode < 1 || d.Mode > 3 || d.ItemID == 0 || d.Count <= 0 || d.Count > 65535 || d.RefundItemID == 0 || d.RefundCount <= 0 || d.RefundCount > 65535 || d.PaidAt.Unix() <= 0 || len(b.MemberAccounts) == 0 {
		return raid.ErrRewardReceipt
	}
	key := b.InstanceID + ":entry-refund"
	if b.Mode == 3 {
		key = b.InstanceID + ":retry"
	}
	if s.antonRewardFlows[key] != nil {
		return nil
	}
	ended := b.Deadline
	if !b.FiendWarEndedAt.IsZero() {
		ended = b.FiendWarEndedAt
	}
	if ended.IsZero() || ended.Before(d.PaidAt) || now.Before(ended) {
		return raid.ErrRewardReceipt
	}
	f := &currentAntonRewardFlow{EntryRefund: &d, InstanceID: b.InstanceID, RaidKey: state.RaidKey, Mode: b.Mode, Phase: b.Phase, CompletedAt: ended, Party: map[uint16]*currentAntonRewardCard{}}
	for id, account := range b.MemberAccounts {
		if id == 0 || account == "" {
			return raid.ErrRewardOwnership
		}
		f.Recipients = append(f.Recipients, currentAntonRewardRecipient{CharacterID: id, AccountID: account, Group: b.MemberGroups[id]})
	}
	sort.Slice(f.Recipients, func(i, j int) bool { return f.Recipients[i].CharacterID < f.Recipients[j].CharacterID })
	if s.antonRewardFlows == nil {
		s.antonRewardFlows = map[string]*currentAntonRewardFlow{}
	}
	s.antonRewardFlows[key] = f
	return nil
}

func (s *Service) advanceCurrentRaidEntryRefundFlow(f *currentAntonRewardFlow, catalog *pvfDungeonDropCatalog, repos repo.Group, now time.Time) error {
	if f == nil || f.EntryRefund == nil {
		return raid.ErrRewardReceipt
	}
	d := *f.EntryRefund
	var failures []error
	for _, recipient := range f.Recipients {
		s.raidMu.Lock()
		card := f.Party[recipient.CharacterID]
		done := card != nil && card.Committed
		s.raidMu.Unlock()
		if done {
			continue
		}
		cmd := raid.RewardCommand{CharacterID: strconv.Itoa(int(recipient.CharacterID)), InstanceID: d.InstanceID, Mode: d.Mode, Phase: f.Phase, Kind: 4, CompletedAt: f.CompletedAt}
		rows, err := resolveCurrentBoosterRewards(catalog, map[uint32]uint32{d.RefundItemID: uint32(d.RefundCount)}, nil, repo.ItemStack{}, f.CompletedAt)
		var attachments []repo.MailAttachment
		if err == nil {
			for _, row := range rows {
				var part []repo.MailAttachment
				part, err = buildCurrentAntonOverflowAttachments(catalog, row, cmd)
				if err != nil {
					break
				}
				attachments = append(attachments, part...)
			}
		}
		var status raid.EntryRefundStatus
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			status, err = raid.RefundEntryByMail(ctx, repos, raid.EntryRefundCommand{Debit: d, AccountID: recipient.AccountID, CharacterID: cmd.CharacterID, FailedAt: f.CompletedAt, Attachments: attachments})
			cancel()
		}
		s.raidMu.Lock()
		if err == nil {
			f.Party[recipient.CharacterID] = &currentAntonRewardCard{Committed: true}
		} else {
			f.NextAttempt = now.Add(2 * time.Second)
		}
		s.raidMu.Unlock()
		if err != nil {
			failures = append(failures, fmt.Errorf("raid %d character %d entry refund: %w", f.RaidKey, recipient.CharacterID, err))
			continue
		}
		s.logGameEvent(nil, "game-raid-entry-refund-committed", "raid_key", f.RaidKey, "instance_id", d.InstanceID, "char_id", recipient.CharacterID, "mail_id", status.MailID, "item_id", d.RefundItemID, "count", d.RefundCount)
		if p, ok := s.onlineGameSession(recipient.CharacterID); ok {
			id, account := recipient.CharacterID, recipient.AccountID
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
				defer cancel()
				_ = s.callGameSession(ctx, p, "raid failure refund mailbox notification", func() error {
					if p.selectedCharacterID != id || s.accountIDForSession(p) != account {
						return nil
					}
					return s.sendMailboxAlarmForSession(p, id, "raid_entry_refund")
				})
			}()
		}
	}
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	s.raidMu.Lock()
	f.FinishedAt = now
	s.raidMu.Unlock()
	return nil
}
