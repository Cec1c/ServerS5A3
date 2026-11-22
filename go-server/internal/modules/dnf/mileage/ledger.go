// Package mileage owns the account-shared event266 points ledger. Callers must
// hold the existing account/character asset transaction when mutating it.
package mileage

import (
	"encoding/json"
	"errors"
	"math"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

const MetadataKey = "event_mileage_v1"
const DailyClearLimit = 50
const MaximumCashEarned = 20000

var ErrInvalid = errors.New("invalid mileage state or purchase")
var ErrInsufficient = errors.New("insufficient mileage")
var ErrReplay = errors.New("mileage purchase already committed")

type Receipt struct {
	Session string `json:"session"`
	Serial  uint64 `json:"serial"`
}
type State struct {
	Version     int                `json:"version"`
	Balance     int64              `json:"balance"`
	CashEarned  int64              `json:"cashEarned"`
	Remainder   int64              `json:"remainder"`
	Day         string             `json:"day"`
	DailyEarned int                `json:"dailyEarned"`
	Purchases   map[uint32]uint32  `json:"purchases,omitempty"`
	Receipts    map[string]Receipt `json:"receipts,omitempty"`
}

func Load(a r.AccountRecord) (State, error) {
	s := State{Version: 1}
	if raw := a.Metadata[MetadataKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return State{}, ErrInvalid
		}
	}
	if s.Version != 1 || s.Balance < 0 || s.Balance > math.MaxInt32 || s.CashEarned < 0 || s.CashEarned > MaximumCashEarned || s.Remainder < 0 || s.Remainder >= 100 || s.DailyEarned < 0 || s.DailyEarned > DailyClearLimit {
		return State{}, ErrInvalid
	}
	if s.Day != "" {
		if d, e := time.Parse("2006-01-02", s.Day); e != nil || d.Format("2006-01-02") != s.Day {
			return State{}, ErrInvalid
		}
	} else if s.DailyEarned != 0 {
		return State{}, ErrInvalid
	}
	for character, receipt := range s.Receipts {
		if character == "" || receipt.Session == "" || receipt.Serial == 0 {
			return State{}, ErrInvalid
		}
	}
	return s, nil
}
func Save(a *r.AccountRecord, s State) error {
	if a == nil {
		return ErrInvalid
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if a.Metadata == nil {
		a.Metadata = map[string]string{}
	}
	a.Metadata[MetadataKey] = string(b)
	return nil
}

// AccrueSpend uses the actual debit from all three wallets, after discounts.
// The hundredths carry across purchases. Redeeming points never resets the cap.
func AccrueSpend(a *r.AccountRecord, spent int64, cap uint32) error {
	if a == nil || spent < 0 || cap == 0 || cap > MaximumCashEarned {
		return ErrInvalid
	}
	s, err := Load(*a)
	if err != nil {
		return err
	}
	if spent == 0 || s.CashEarned >= int64(cap) {
		return nil
	}
	points := (spent/100)*3 + (spent%100*3+s.Remainder)/100
	remainder := (spent%100*3 + s.Remainder) % 100
	points = min(points, int64(cap)-s.CashEarned)
	if s.Balance > math.MaxInt32-points {
		return ErrInvalid
	}
	s.Balance += points
	s.CashEarned += points
	s.Remainder = remainder
	if s.CashEarned >= int64(cap) {
		s.Remainder = 0
	}
	return Save(a, s)
}

// AccrueClear is invoked only after the durable recommended-clear activity
// receipt was inserted in the SAME settlement transaction, including zero-point
// closed/capped clears. Replaying an old clear cannot award it after a reset.
func AccrueClear(a *r.AccountRecord, now time.Time, enabled bool) error {
	if a == nil {
		return ErrInvalid
	}
	s, err := Load(*a)
	if err != nil {
		return err
	}
	day := now.In(time.FixedZone("CST", 8*3600)).Add(-6 * time.Hour).Format("2006-01-02")
	if s.Day > day {
		return ErrInvalid
	}
	if s.Day != day {
		s.Day = day
		s.DailyEarned = 0
	}
	if enabled && s.DailyEarned < DailyClearLimit {
		if s.Balance == math.MaxInt32 {
			return ErrInvalid
		}
		s.Balance++
		s.DailyEarned++
	}
	return Save(a, s)
}

type Product struct{ Commodity, Price, Count, Limit uint32 }

// Redeem reserves the whole server-resolved cart. Item grants and this state
// are committed together by the asset owner, or are all rolled back.
func Redeem(a *r.AccountRecord, character, session string, serial uint64, cart []Product) error {
	if a == nil || character == "" || session == "" || serial == 0 || len(cart) == 0 || len(cart) > 33 {
		return ErrInvalid
	}
	s, err := Load(*a)
	if err != nil {
		return err
	}
	if old := s.Receipts[character]; old.Session == session && serial <= old.Serial {
		return ErrReplay
	}
	if s.Purchases == nil {
		s.Purchases = map[uint32]uint32{}
	}
	var cost int64
	for _, p := range cart {
		if p.Commodity == 0 || p.Price == 0 || p.Count == 0 {
			return ErrInvalid
		}
		cost += int64(p.Price)
		count := s.Purchases[p.Commodity]
		if count == math.MaxUint32 || (p.Limit != 0 && count >= p.Limit) {
			return ErrInvalid
		}
		s.Purchases[p.Commodity] = count + 1
	}
	if s.Balance < cost {
		return ErrInsufficient
	}
	s.Balance -= cost
	if s.Receipts == nil {
		s.Receipts = map[string]Receipt{}
	}
	s.Receipts[character] = Receipt{session, serial}
	return Save(a, s)
}
