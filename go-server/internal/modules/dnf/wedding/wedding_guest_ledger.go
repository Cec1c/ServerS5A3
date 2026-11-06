package wedding

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

const GuestIndexScope = r.WeddingGuestIndexScope
const guestLedgerPrefix = "guest_v1:"

type GuestRunReference struct {
	Hosts  [2]string `json:"hosts"`
	At     int64     `json:"at"`
	Closed bool      `json:"closed,omitempty"`
}

func closeGuestRun(ctx context.Context, repo r.SettingsRepository, ledger GuestLedger, now time.Time) error {
	record, index, err := loadGuestIndex(ctx, repo)
	if err != nil {
		return err
	}
	ref, found := index.Runs[ledger.Key]
	if !found || ref.At != ledger.At || ref.Hosts != ledger.Hosts {
		return ErrState
	}
	if ref.Closed {
		return nil
	}
	ref.Closed = true
	index.Runs[ledger.Key] = ref
	raw, err := json.Marshal(index)
	if err != nil {
		return err
	}
	record = r.CloneSettings(record)
	if record.Values == nil {
		record.Values = map[string]string{}
	}
	record.Values["index"], record.UpdatedAt = string(raw), now
	return repo.Save(ctx, record)
}

type GuestIndex struct {
	Version int                          `json:"version"`
	Next    uint32                       `json:"next"`
	Runs    map[uint32]GuestRunReference `json:"runs"`
}
type GuestInvitation struct {
	GuestID    string `json:"guest_id"`
	InviterID  string `json:"inviter_id"`
	InvitedAt  int64  `json:"invited_at"`
	Gold       int64  `json:"gold"`
	Wish       string `json:"wish,omitempty"`
	PaidAt     int64  `json:"paid_at,omitempty"`
	Settled    bool   `json:"settled,omitempty"`
	Refunded   bool   `json:"refunded,omitempty"`
	RewardMail string `json:"reward_mail,omitempty"`
	RefundMail string `json:"refund_mail,omitempty"`
}
type ReservationPayment struct {
	PayerID     string             `json:"payer_id"`
	Gold        int64              `json:"gold"`
	TicketID    int64              `json:"ticket_id"`
	TicketCount int64              `json:"ticket_count"`
	Refunded    bool               `json:"refunded"`
	Tickets     []r.MailAttachment `json:"tickets,omitempty"`
}
type GuestLedger struct {
	Version             int       `json:"version"`
	Key                 uint32    `json:"key"`
	Hosts               [2]string `json:"hosts"`
	At                  int64     `json:"at"`
	Channel, Area, Kind byte
	Capacity            int                        `json:"capacity"`
	Invitations         map[string]GuestInvitation `json:"invitations"`
	Settled             bool                       `json:"settled"`
	Refunded            bool                       `json:"refunded"`
	FeeGold             int64                      `json:"fee_gold"`
	HostGold            [2]int64                   `json:"host_gold"`
	RemainderGold       int64                      `json:"remainder_gold"`
	ReservationPayment  ReservationPayment         `json:"reservation_payment"`
}

func guestLedgerKey(at int64) string { return guestLedgerPrefix + strconv.FormatInt(at, 10) }

func decodeGuestLedger(record r.SettingsRecord, at int64) (GuestLedger, bool, error) {
	var ledger GuestLedger
	raw := record.Values[guestLedgerKey(at)]
	if raw == "" {
		return ledger, false, nil
	}
	if err := json.Unmarshal([]byte(raw), &ledger); err != nil {
		return ledger, false, err
	}
	if ledger.Version != 1 || ledger.Key == 0 || ledger.At != at || ledger.Capacity <= 0 || ledger.Capacity > 65535 || ledger.Invitations == nil || len(ledger.Invitations) > ledger.Capacity*2 {
		return ledger, false, ErrState
	}
	scope, err := r.WeddingPairScope(ledger.Hosts[:])
	if err != nil || scope != record.Scope {
		return ledger, false, ErrOwnership
	}
	if ledger.Settled && ledger.Refunded {
		return ledger, false, ErrState
	}
	payment := ledger.ReservationPayment
	if payment.PayerID != "" && payment.PayerID != ledger.Hosts[0] && payment.PayerID != ledger.Hosts[1] || payment.Gold < 0 || payment.TicketID < 0 || payment.TicketCount < 0 || (payment.TicketID == 0) != (payment.TicketCount == 0) {
		return ledger, false, ErrState
	}
	counts := map[string]int{}
	var total int64
	for id, inv := range ledger.Invitations {
		if _, err := NumericID(id); err != nil {
			return ledger, false, ErrState
		}
		if id == ledger.Hosts[0] || id == ledger.Hosts[1] || inv.GuestID != id || (inv.InviterID != ledger.Hosts[0] && inv.InviterID != ledger.Hosts[1]) || inv.Gold < 0 || inv.Gold > 0x7fffffff || inv.Settled && inv.Refunded {
			return ledger, false, ErrState
		}
		counts[inv.InviterID]++
		if counts[inv.InviterID] > ledger.Capacity {
			return ledger, false, ErrState
		}
		if ledger.Settled && !inv.Settled || ledger.Refunded && !inv.Refunded {
			return ledger, false, ErrState
		}
		total += inv.Gold
	}
	if ledger.Settled && (ledger.FeeGold < 0 || ledger.HostGold[0] < 0 || ledger.HostGold[1] < 0 || ledger.RemainderGold < 0 || ledger.RemainderGold > 1 || ledger.FeeGold+ledger.HostGold[0]+ledger.HostGold[1]+ledger.RemainderGold != total) {
		return ledger, false, ErrState
	}
	return ledger, true, nil
}

func saveGuestLedger(ctx context.Context, repo r.SettingsRepository, record r.SettingsRecord, ledger GuestLedger, now time.Time) error {
	raw, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	record = r.CloneSettings(record)
	if record.Values == nil {
		record.Values = map[string]string{}
	}
	record.Values[guestLedgerKey(ledger.At)] = string(raw)
	record.UpdatedAt = now
	return repo.Save(ctx, record)
}

func loadGuestIndex(ctx context.Context, repo r.SettingsRepository) (r.SettingsRecord, GuestIndex, error) {
	record, _, err := repo.Load(ctx, GuestIndexScope)
	if err != nil {
		return record, GuestIndex{}, err
	}
	record.Scope = GuestIndexScope
	index := GuestIndex{Version: 1, Runs: map[uint32]GuestRunReference{}}
	if raw := record.Values["index"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &index); err != nil {
			return record, index, err
		}
	}
	if index.Version != 1 || index.Runs == nil {
		return record, index, ErrState
	}
	return record, index, nil
}

func (o Owner) ResolveGuestWedding(ctx context.Context, key uint32) (GuestRunReference, error) {
	if key == 0 || o.Repositories.Settings == nil {
		return GuestRunReference{}, ErrState
	}
	_, index, err := loadGuestIndex(ctx, o.Repositories.Settings)
	if err != nil {
		return GuestRunReference{}, err
	}
	ref, found := index.Runs[key]
	if !found || ref.At <= 0 {
		return ref, ErrState
	}
	if _, err := r.WeddingPairScope(ref.Hosts[:]); err != nil {
		return GuestRunReference{}, ErrState
	}
	return ref, nil
}

func (o Owner) ReadGuestLedger(ctx context.Context, hosts []string, at int64) (GuestLedger, bool, error) {
	scope, err := r.WeddingPairScope(hosts)
	if err != nil || o.Repositories.Settings == nil {
		return GuestLedger{}, false, ErrUnavailable
	}
	record, _, err := o.Repositories.Settings.Load(ctx, scope)
	if err != nil {
		return GuestLedger{}, false, err
	}
	record.Scope = scope
	return decodeGuestLedger(record, at)
}

// GuestLedgerHasDebt is safe to call against the already-scoped lifecycle
// settings record before divorce or removal of a historic relationship.
func GuestLedgerHasDebt(record r.SettingsRecord) (bool, error) {
	for key, raw := range record.Values {
		if len(key) < len(guestLedgerPrefix) || key[:len(guestLedgerPrefix)] != guestLedgerPrefix {
			continue
		}
		var ledger GuestLedger
		if err := json.Unmarshal([]byte(raw), &ledger); err != nil {
			return false, err
		}
		if ledger.Version != 1 {
			return false, ErrState
		}
		if !ledger.Settled && !ledger.ReservationPayment.Refunded && ledger.ReservationPayment.PayerID != "" && (ledger.ReservationPayment.Gold > 0 || ledger.ReservationPayment.TicketCount > 0) {
			return true, nil
		}
		for _, invitation := range ledger.Invitations {
			if invitation.Gold > 0 && !invitation.Settled && !invitation.Refunded {
				return true, nil
			}
		}
	}
	return false, nil
}

type GuestError int32

func (e GuestError) Error() string { return fmt.Sprintf("wedding guest result %d", int32(e)) }

func (ledger GuestLedger) RemainingFor(host string) int {
	if host != ledger.Hosts[0] && host != ledger.Hosts[1] {
		return 0
	}
	used := 0
	for _, inv := range ledger.Invitations {
		if inv.InviterID == host {
			used++
		}
	}
	return max(ledger.Capacity-used, 0)
}
