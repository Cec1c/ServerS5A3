package wedding

import (
	"context"
	"fmt"

	r "longheng.io/server/internal/modules/dnf/repository"
)

// Settlement semantics use the documented wedding policy: PVF charge is a
// percentage fee; the remaining total is split equally. An odd coin remains
// explicitly accounted for in the immutable receipt. Each gold mail is <=100M.
const guestGoldMailLimit int64 = 100000000

type GuestSettlementResult struct {
	Ledger         GuestLedger
	MailRecipients []string
	Replay         bool
}

func appendGuestGoldMails(ctx context.Context, g r.Group, guest r.CharacterRecord, amount int64, key uint32, kind, title, body string, req PairRequest) error {
	for part := 0; amount > 0; part++ {
		gold := min(amount, guestGoldMailLimit)
		_, err := r.AppendSystemMail(ctx, g.Mailbox, r.SystemMailDelivery{RecipientCharacterID: guest.CharacterID, RecipientName: guest.Name, Gold: gold,
			Title: title, Body: body, Source: "wedding_guest_" + kind, CompletionKey: guestCompletionKey(key, kind, guest.CharacterID) + fmt.Sprintf(":%d", part), CreatedAt: req.Now})
		if err != nil {
			return err
		}
		amount -= gold
	}
	return nil
}

func (o Owner) FinalizeGuestWedding(ctx context.Context, req PairRequest, at int64, rules *GuestRules) (GuestSettlementResult, error) {
	var result GuestSettlementResult
	hosts := []string{req.Actor.CharacterID, req.Partner.CharacterID}
	snapshot, found, err := o.ReadGuestLedger(ctx, hosts, at)
	if err != nil || !found {
		return result, err
	}
	if rules == nil || rules.Charge < 0 || rules.Charge > 100 {
		return result, ErrUnavailable
	}
	err = o.guestMutation(ctx, hosts, guestIDs(snapshot), req.Now, func(g r.Group, chars map[string]r.CharacterRecord, record r.SettingsRecord) error {
		_, _, state, err := guestHosts(req, chars)
		if err != nil || state.Phase != Married || state.CeremonyAt != at || state.MarriedAt < at || req.Now.Unix() < state.MarriedAt {
			return ErrState
		}
		ledger, exists, err := decodeGuestLedger(record, at)
		if err != nil {
			return err
		}
		if !exists || ledger.Key != snapshot.Key || !sameGuestIDs(snapshot, ledger) {
			return ErrState
		}
		if ledger.Refunded {
			return ErrState
		}
		if ledger.Settled {
			result = GuestSettlementResult{Ledger: ledger, Replay: true}
			return closeGuestRun(ctx, g.Settings, ledger, req.Now)
		}
		var total int64
		for _, inv := range ledger.Invitations {
			if inv.Refunded || inv.Settled || inv.Gold < 0 || inv.Gold > rules.MaximumGold {
				return ErrState
			}
			total += inv.Gold
		}
		fee := total * rules.Charge / 100
		share := (total - fee) / 2
		ledger.FeeGold, ledger.HostGold, ledger.RemainderGold = fee, [2]int64{share, share}, total-fee-share*2
		for _, id := range ledger.Hosts {
			if err := appendGuestGoldMails(ctx, g, chars[id], share, ledger.Key, "host", "婚礼礼金", "婚礼已完成，礼金按手续费规则结算后平均分配。", req); err != nil {
				return err
			}
			if share > 0 {
				result.MailRecipients = append(result.MailRecipients, id)
			}
		}
		for _, id := range guestIDs(ledger) {
			inv := ledger.Invitations[id]
			if inv.Gold > 0 {
				rewards, err := rules.GiftFor(inv.Gold, ledger.Kind)
				if err != nil {
					return err
				}
				mail, err := r.AppendSystemMail(ctx, g.Mailbox, r.SystemMailDelivery{RecipientCharacterID: id, RecipientName: chars[id].Name, Attachments: rewards,
					Title: "婚礼回礼", Body: "感谢您参加婚礼并赠送祝福。", Source: "wedding_guest_return_gift", CompletionKey: guestCompletionKey(ledger.Key, "reward", id), CreatedAt: req.Now})
				if err != nil {
					return err
				}
				inv.RewardMail = mail
				result.MailRecipients = append(result.MailRecipients, id)
			}
			inv.Settled = true
			ledger.Invitations[id] = inv
		}
		ledger.Settled = true
		if req.Revalidate != nil && !req.Revalidate() {
			return ErrOwnership
		}
		if err := saveGuestLedger(ctx, g.Settings, record, ledger, req.Now); err != nil {
			return err
		}
		result.Ledger = ledger
		return closeGuestRun(ctx, g.Settings, ledger, req.Now)
	})
	if err != nil {
		return GuestSettlementResult{}, err
	}
	return result, nil
}

// A failed/unattended reservation refunds the exact escrow without a fee.
// The immutable run key and all mail receipts remain after cancellation.
func (o Owner) RefundGuestWedding(ctx context.Context, req PairRequest, at int64) (GuestSettlementResult, error) {
	var result GuestSettlementResult
	hosts := []string{req.Actor.CharacterID, req.Partner.CharacterID}
	snapshot, found, err := o.ReadGuestLedger(ctx, hosts, at)
	if err != nil || !found {
		return result, err
	}
	err = o.guestMutation(ctx, hosts, guestIDs(snapshot), req.Now, func(g r.Group, chars map[string]r.CharacterRecord, record r.SettingsRecord) error {
		// A later valid cancellation can already have retired both links. Debt
		// still belongs to these immutable host IDs/accounts, not their present
		// spouse relationship. Never make refund recovery depend on re-engaging.
		if chars[req.Actor.CharacterID].AccountID != req.Actor.AccountID || chars[req.Partner.CharacterID].AccountID != req.Partner.AccountID || req.Revalidate != nil && !req.Revalidate() {
			return ErrOwnership
		}
		for _, id := range hosts {
			state, err := Read(chars[id])
			if err != nil {
				return err
			}
			if state.CeremonyAt == at && (state.Phase == Reserved || state.Phase == InCeremony || state.Phase == Married) {
				return ErrState
			}
		}
		ledger, exists, err := decodeGuestLedger(record, at)
		if err != nil {
			return err
		}
		if !exists || ledger.Key != snapshot.Key || !sameGuestIDs(snapshot, ledger) {
			return ErrState
		}
		if ledger.Settled {
			return ErrState
		}
		if ledger.Refunded {
			result = GuestSettlementResult{Ledger: ledger, Replay: true}
			return closeGuestRun(ctx, g.Settings, ledger, req.Now)
		}
		payment := ledger.ReservationPayment
		if payment.PayerID != "" && !payment.Refunded {
			payer := chars[payment.PayerID]
			if err := appendGuestGoldMails(ctx, g, payer, payment.Gold, ledger.Key, "reservation_refund", "婚礼费用退回", "本次婚礼未能举行，原申请费用全额退回。", req); err != nil {
				return err
			}
			if payment.TicketCount > 0 {
				var count int64
				for _, ticket := range payment.Tickets {
					if ticket.ItemID != payment.TicketID || ticket.Count <= 0 {
						return ErrState
					}
					count += ticket.Count
				}
				if count != payment.TicketCount {
					return ErrState
				}
				if _, err := r.AppendSystemMail(ctx, g.Mailbox, r.SystemMailDelivery{RecipientCharacterID: payer.CharacterID, RecipientName: payer.Name, Title: "婚礼凭证退回", Body: "本次婚礼未能举行，原付费婚礼凭证退回。", Source: "wedding_reservation_ticket_refund", CompletionKey: guestCompletionKey(ledger.Key, "ticket_refund", payer.CharacterID), Attachments: payment.Tickets, CreatedAt: req.Now}); err != nil {
					return err
				}
			}
			if payment.Gold > 0 || payment.TicketCount > 0 {
				result.MailRecipients = append(result.MailRecipients, payer.CharacterID)
			}
			payment.Refunded = true
			ledger.ReservationPayment = payment
		}
		for _, id := range guestIDs(ledger) {
			inv := ledger.Invitations[id]
			if inv.Settled || inv.Refunded || inv.Gold < 0 {
				return ErrState
			}
			if inv.Gold > 0 {
				if err := appendGuestGoldMails(ctx, g, chars[id], inv.Gold, ledger.Key, "refund", "婚礼礼金退回", "本次婚礼未能举行，礼金全额退回。", req); err != nil {
					return err
				}
				result.MailRecipients = append(result.MailRecipients, id)
			}
			inv.Refunded = true
			ledger.Invitations[id] = inv
		}
		ledger.Refunded = true
		if req.Revalidate != nil && !req.Revalidate() {
			return ErrOwnership
		}
		if err := saveGuestLedger(ctx, g.Settings, record, ledger, req.Now); err != nil {
			return err
		}
		result.Ledger = ledger
		return closeGuestRun(ctx, g.Settings, ledger, req.Now)
	})
	if err != nil {
		return GuestSettlementResult{}, err
	}
	return result, nil
}
