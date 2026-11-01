package activeness

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/cerashop"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"time"
)

const prefix = "private_activity_v1_"

func Day(now time.Time) int64 {
	t := now.In(time.FixedZone("UTC+8", 8*3600)).Add(-6 * time.Hour)
	return int64(t.Year()*10000 + int(t.Month())*100 + t.Day())
}

type Snapshot struct {
	Day, Fingerprint int64
	Points           uint16
	Claimed          uint8
	Progress         [8]uint16
	Tasks            [8]Task
	Rewards          [3]Reward
	Replayed         bool
}

func initState(ch *repo.CharacterRecord, c *Catalog, now time.Time) error {
	if ch == nil || c == nil || c.Fingerprint <= 0 || now.IsZero() {
		return ErrState
	}
	if ch.Stats == nil {
		ch.Stats = map[string]int64{}
	}
	v := ch.Stats
	old := v[prefix+"day"]
	day := Day(now)
	if old < 0 || old > day {
		return ErrState
	}
	if old != 0 {
		s := strconv.FormatInt(old, 10)
		t, e := time.Parse("20060102", s)
		if e != nil || t.Format("20060102") != s {
			return ErrState
		}
	}
	if old != day {
		for _, k := range []string{"points", "claimed", "done", "rules011_prior_claimed"} {
			v[prefix+k] = 0
		}
		for i := 1; i <= 8; i++ {
			v[progressKey(i)] = 0
		}
		v[prefix+"day"] = day
		v[prefix+"fingerprint"] = c.Fingerprint
	}
	if v[prefix+"fingerprint"] != c.Fingerprint {
		if err := migrateRewards006(ch, c, now); err != nil {
			return err
		}
	}
	if v[prefix+"claimed"] < 0 || v[prefix+"claimed"] > 7 || v[prefix+"done"] < 0 || v[prefix+"done"] > 255 {
		return ErrState
	}
	points := int64(0)
	for i, t := range c.Tasks {
		p := v[progressKey(i+1)]
		done := v[prefix+"done"]&(1<<i) != 0
		if p < 0 || p > int64(t.Target) || done != (p == int64(t.Target)) {
			return ErrState
		}
		if done {
			points += int64(t.Points)
		}
	}
	if points != v[prefix+"points"] {
		return ErrState
	}
	for i, r := range c.Rewards {
		prior := v[prefix+"rules011_prior_claimed"]
		if prior < 0 || prior > 7 || prior & ^v[prefix+"claimed"] != 0 || (prior != 0 && c.Fingerprint != rules011NewFingerprint && c.Fingerprint != rewards006Fingerprint) {
			return ErrState
		}
		if v[prefix+"claimed"]&(1<<i) != 0 && prior&(1<<i) == 0 && points < int64(r.Required) {
			return ErrState
		}
	}
	return nil
}
func progressKey(id int) string { return prefix + "progress_" + strconv.Itoa(id) }

// Apply is called inside the same authoritative transaction as fatigue debit
// or lucky-box consumption. Completed dungeon events use a frozen event time.
// No public request accepts an event or a point amount.
func Apply(ch *repo.CharacterRecord, c *Catalog, id uint16, amount uint32, now, eventAt time.Time) (bool, error) {
	if c == nil {
		return false, nil
	}
	if id < 1 || id > 8 || amount == 0 || eventAt.IsZero() || eventAt.After(now) {
		return false, ErrState
	}
	if err := initState(ch, c, now); err != nil {
		return false, err
	}
	if Day(eventAt) != Day(now) {
		return false, nil
	}
	t := c.Tasks[id-1]
	key := progressKey(int(id))
	before := ch.Stats[key]
	if before == int64(t.Target) {
		return false, nil
	}
	after := min(int64(t.Target), before+int64(amount))
	ch.Stats[key] = after
	if after == int64(t.Target) {
		ch.Stats[prefix+"done"] |= 1 << (id - 1)
		ch.Stats[prefix+"points"] += int64(t.Points)
	}
	return true, nil
}
func view(ch repo.CharacterRecord, c *Catalog) Snapshot {
	s := Snapshot{Day: ch.Stats[prefix+"day"], Fingerprint: c.Fingerprint, Points: uint16(ch.Stats[prefix+"points"]), Claimed: uint8(ch.Stats[prefix+"claimed"]), Tasks: c.Tasks, Rewards: c.Rewards}
	for i := range s.Progress {
		s.Progress[i] = uint16(ch.Stats[progressKey(i+1)])
	}
	return s
}

type Command struct {
	AccountID, CharacterID           string
	Now                              time.Time
	Claim                            uint16
	ExpectedDay, ExpectedFingerprint int64
	Validate                         func() error
	Deliver                          func(context.Context, repo.MailboxRepository, Reward, string) (string, error)
}

// Settle atomically commits the account happy-token balance, character claim
// bit and physical reward mail. Full mailbox, balance overflow or stale session
// rolls all three back. Query and retries never create extra rewards.
func Settle(ctx context.Context, g repo.Group, c *Catalog, cmd Command) (Snapshot, error) {
	u, ok := g.AccountAssets.(repo.AccountCharacterLedgerMailboxAssetUnitOfWork)
	if !ok || c == nil || cmd.AccountID == "" || cmd.CharacterID == "" || cmd.Now.IsZero() || cmd.Claim > 3 || cmd.Deliver == nil {
		return Snapshot{}, ErrOwner
	}
	if cmd.Claim > 0 && (cmd.ExpectedDay != Day(cmd.Now) || cmd.ExpectedFingerprint != c.Fingerprint) {
		return Snapshot{}, ErrStaleDay
	}
	var out Snapshot
	err := u.WithinAccountCharacterLedgerMailboxAssets(ctx, cmd.AccountID, cmd.CharacterID, func(accounts repo.AccountRepository, _ repo.AccountInventoryRepository, chars repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository, mail repo.MailboxRepository) error {
		if cmd.Validate != nil {
			if err := cmd.Validate(); err != nil {
				return err
			}
		}
		ch, found, err := chars.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || ch.AccountID != cmd.AccountID || ch.CharacterID != cmd.CharacterID {
			return ErrOwner
		}
		ch = repo.CloneCharacter(ch)
		if err := initState(&ch, c, cmd.Now); err != nil {
			return err
		}
		out = view(ch, c)
		if cmd.Claim > 0 {
			mask := uint8(1 << (cmd.Claim - 1))
			if out.Claimed&mask != 0 {
				out.Replayed = true
			} else {
				r := c.Rewards[cmd.Claim-1]
				if out.Points < r.Required {
					return ErrNotReady
				}
				account, found, err := accounts.Load(ctx, cmd.AccountID)
				if err != nil {
					return err
				}
				if !found || account.AccountID != cmd.AccountID {
					return ErrOwner
				}
				account = repo.CloneAccount(account)
				if _, err := cerashop.CreditVoucherCounts(&account, cerashop.VoucherCounts{HappyTokenCera: int64(r.HappyTokens)}); err != nil {
					return err
				}
				key := fmt.Sprintf("private_activity:%d:stage:%d", out.Day, r.ID)
				mailID, err := cmd.Deliver(ctx, mail, r, key)
				if err != nil {
					return err
				}
				if mailID == "" {
					return ErrState
				}
				account.UpdatedAt = cmd.Now
				if err := accounts.Save(ctx, account); err != nil {
					return err
				}
				out.Claimed |= mask
				ch.Stats[prefix+"claimed"] = int64(out.Claimed)
			}
		}
		if cmd.Validate != nil {
			if err := cmd.Validate(); err != nil {
				return err
			}
		}
		ch.UpdatedAt = cmd.Now
		return repo.SaveCharacterFields(ctx, chars, ch, repo.CharacterFieldStats)
	})
	if err != nil {
		return Snapshot{}, err
	}
	return out, nil
}
