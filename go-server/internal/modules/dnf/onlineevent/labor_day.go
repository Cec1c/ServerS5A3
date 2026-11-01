package onlineevent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const LaborDayMetadataKey = "event_labor_day_2348_v1"
const LaborDayDuration = 30 * 24 * time.Hour
const LaborDayPVFPath = "event/chn_event/chn_playdnfatlaborday.evt"

// The 2016 NPK advertises three login mails and ten daily 10-minute gifts.
// Original distribution: https://dnf.qq.com/cp/a20160421burning/
// Daily stages 1..4 are basic, 5..9 intermediate, and 10 advanced.
type LaborDayPolicy struct {
	Starts time.Time `json:"starts"`
	Ends   time.Time `json:"ends"`
}

func (p LaborDayPolicy) ActiveAt(at time.Time) bool {
	return (ActivityWindow{Starts: p.Starts, Ends: p.Ends}).ActiveAt(at)
}
func (p LaborDayPolicy) valid() bool {
	return !p.Starts.IsZero() && (p.Ends.IsZero() || p.Ends.After(p.Starts))
}

type LaborDayCatalog struct {
	Policy LaborDayPolicy
	Login  [3]AttendancePVFItemReward
	Online [3]AttendancePVFItemReward
}

func LoadLaborDayCatalog(ctx context.Context, source dnfpvf.Source) (*LaborDayCatalog, error) {
	if source == nil {
		return nil, ErrOwnerUnavailable
	}
	c := &LaborDayCatalog{}
	text, err := source.ReadText(LaborDayPVFPath)
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse(LaborDayPVFPath, text)
	if err != nil {
		return nil, err
	}
	login, ok := doc.Section("continuous login reward")
	if !ok || len(login) != 6 {
		return nil, fmt.Errorf("labor day login reward section has %d tokens", len(login))
	}
	for i := range c.Login {
		id, count := login[i*2], login[i*2+1]
		if id.Kind != dnfpvf.TokenInt || count.Kind != dnfpvf.TokenInt || id.Int <= 0 || count.Int <= 0 {
			return nil, fmt.Errorf("labor day login reward %d is invalid", i)
		}
		c.Login[i] = AttendancePVFItemReward{ItemID: id.Int, Count: count.Int}
		c.Online[i] = AttendancePVFItemReward{ItemID: int64(490001502 + i), Count: 1}
	}
	rewards := append(append([]AttendancePVFItemReward{}, c.Login[:]...), c.Online[:]...)
	paths, err := resolveAttendanceRewardDefinitions(ctx, source, rewards)
	if err != nil {
		return nil, err
	}
	attachAttendanceDefinitionPaths(c.Login[:], paths)
	attachAttendanceDefinitionPaths(c.Online[:], paths)
	// Online boxes must belong to this Labor Day edition. Login boxes are the
	// older 10005051..53 PVF stackables and have a different title by design.
	for _, r := range c.Online {
		t, err := source.ReadText(r.DefinitionPath)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(t, "惊喜礼盒") {
			return nil, fmt.Errorf("labor day online item %d edition mismatch", r.ItemID)
		}
	}
	return c, nil
}

type laborDayProgress struct {
	Intervals []creditedInterval `json:"intervals,omitempty"`
	Paid      uint32             `json:"paid"`
}
type laborDayState struct {
	Version   int                         `json:"version"`
	Starts    time.Time                   `json:"starts"`
	LoginDays []string                    `json:"login_days"`
	Days      map[string]laborDayProgress `json:"days"`
}
type LaborDaySnapshot struct {
	LoginDays, TodaySeconds, TodayPaid uint32
	MailIDs                            []string
}
type LaborDayCommand struct {
	AccountID, CharacterID string
	Now                    time.Time
	Validate               func() bool
	From, To               time.Time
	Login                  bool
	Deliver                func(dnfrepo.MailboxRepository, AttendancePVFItemReward, string) (string, error)
}

func LaborDayOnlineTier(paid uint32) int {
	if paid < 4 {
		return 0
	}
	if paid < 9 {
		return 1
	}
	return 2
}

// Mail, distinct login days, merged presence intervals and award receipts are
// one account-locked transaction. There is no client-submitted time or claim.
func SettleLaborDay(ctx context.Context, repos dnfrepo.Group, c *LaborDayCatalog, cmd LaborDayCommand) (LaborDaySnapshot, error) {
	var result LaborDaySnapshot
	if c == nil || !c.Policy.valid() || repos.MailboxAssets == nil || cmd.Deliver == nil || cmd.Now.IsZero() || cmd.AccountID == "" || cmd.CharacterID == "" {
		return result, ErrOwnerUnavailable
	}
	if !c.Policy.ActiveAt(cmd.Now) && (cmd.From.IsZero() || !c.Policy.ActiveAt(cmd.From)) {
		return result, nil
	}
	if cmd.From.After(cmd.To) || cmd.To.After(cmd.Now) {
		return result, ErrObservationInvalid
	}
	err := withinPortedEventAssets(ctx, repos, cmd.AccountID, cmd.CharacterID, func(accounts dnfrepo.AccountRepository, characters dnfrepo.CharacterRepository, _ dnfrepo.InventoryRepository, mailboxes dnfrepo.MailboxRepository) error {
		ch, found, err := characters.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || ch.AccountID != cmd.AccountID {
			return ErrAccountMismatch
		}
		a, found, err := accounts.Load(ctx, cmd.AccountID)
		if err != nil {
			return err
		}
		if !found {
			return ErrAccountNotFound
		}
		raw := a.Metadata[LaborDayMetadataKey]
		state := laborDayState{Version: 1, Starts: c.Policy.Starts, Days: map[string]laborDayProgress{}}
		if raw != "" {
			d := json.NewDecoder(strings.NewReader(raw))
			d.DisallowUnknownFields()
			if err := d.Decode(&state); err != nil {
				return err
			}
			if err := rejectTrailingJSON(d); err != nil {
				return err
			}
			if state.Version != 1 || state.Starts.IsZero() || len(state.LoginDays) > 3 || state.Days == nil || len(state.Days) > 31 {
				return ErrStateInvalid
			}
			previous := ""
			for _, day := range state.LoginDays {
				if day <= previous {
					return ErrStateInvalid
				}
				previous = day
				if _, ok := state.Days[day]; !ok {
					return ErrStateInvalid
				}
			}
			first, _ := calendarDay(ChinaCalendar, defaultDailyBoundary, state.Starts)
			last, _ := calendarDay(ChinaCalendar, defaultDailyBoundary, c.Policy.Ends.Add(-time.Nanosecond))
			for day, p := range state.Days {
				if _, err := time.Parse(time.DateOnly, day); err != nil || day < first || (!c.Policy.Ends.IsZero() && day > last) || p.Paid > 10 {
					return ErrStateInvalid
				}
				var end uint32
				var seconds uint32
				for i, v := range p.Intervals {
					if v.Start >= v.End || v.End > 86400 || (i > 0 && v.Start <= end) {
						return ErrStateInvalid
					}
					seconds += v.End - v.Start
					end = v.End
				}
				if p.Paid > seconds/600 {
					return ErrStateInvalid
				}
			}
		}
		deliver := func(reward AttendancePVFItemReward, key string) error {
			id, err := cmd.Deliver(mailboxes, reward, LaborDayMetadataKey+":"+state.Starts.Format(time.RFC3339)+":"+key)
			if err != nil {
				return err
			}
			if id == "" {
				return ErrRewardInvalid
			}
			result.MailIDs = append(result.MailIDs, id)
			return nil
		}
		ensureDay := func(day string) (laborDayProgress, error) {
			if p, ok := state.Days[day]; ok {
				return p, nil
			}
			// Never invent a new login day behind an already observed day.
			for known := range state.Days {
				if day < known {
					return laborDayProgress{}, ErrObservationStale
				}
			}
			if len(state.LoginDays) < 3 {
				if err := deliver(c.Login[len(state.LoginDays)], fmt.Sprintf("login:%d", len(state.LoginDays)+1)); err != nil {
					return laborDayProgress{}, err
				}
				state.LoginDays = append(state.LoginDays, day)
			}
			p := laborDayProgress{}
			state.Days[day] = p
			return p, nil
		}
		from, to := cmd.From, cmd.To
		if from.Before(c.Policy.Starts) {
			from = c.Policy.Starts
		}
		if !c.Policy.Ends.IsZero() && to.After(c.Policy.Ends) {
			to = c.Policy.Ends
		}
		if !cmd.From.IsZero() && from.Before(to) {
			for from.Before(to) {
				day, start := calendarDay(ChinaCalendar, defaultDailyBoundary, from)
				end := start.Add(24 * time.Hour)
				if end.After(to) {
					end = to
				}
				p, err := ensureDay(day)
				if err != nil {
					return err
				}
				u := persistedState{Intervals: p.Intervals}
				for _, v := range p.Intervals {
					u.OnlineSeconds += uint64(v.End - v.Start)
				}
				lo, hi := intervalSeconds(start, from, end)
				u.creditInterval(lo, hi)
				p.Intervals = u.Intervals
				eligible := uint32(u.OnlineSeconds / 600)
				if eligible > 10 {
					eligible = 10
				}
				for p.Paid < eligible {
					tier := LaborDayOnlineTier(p.Paid)
					if err := deliver(c.Online[tier], fmt.Sprintf("online:%s:%d", day, p.Paid+1)); err != nil {
						return err
					}
					p.Paid++
				}
				state.Days[day] = p
				from = end
			}
		}
		if cmd.Login && c.Policy.ActiveAt(cmd.Now) {
			day, _ := calendarDay(ChinaCalendar, defaultDailyBoundary, cmd.Now)
			if _, err := ensureDay(day); err != nil {
				return err
			}
		}
		result.LoginDays = uint32(len(state.LoginDays))
		today, _ := calendarDay(ChinaCalendar, defaultDailyBoundary, cmd.Now)
		p := state.Days[today]
		result.TodayPaid = p.Paid
		for _, v := range p.Intervals {
			result.TodaySeconds += v.End - v.Start
		}
		// 长期活动仅保留最初三次登录及最近28天，最新日期仍阻止旧观察重新建档。
		if len(state.Days) > 31 {
			keys := make([]string, 0, len(state.Days))
			for day := range state.Days {
				keys = append(keys, day)
			}
			sort.Strings(keys)
			keep := map[string]bool{}
			for _, day := range state.LoginDays {
				keep[day] = true
			}
			for _, day := range keys[max(0, len(keys)-28):] {
				keep[day] = true
			}
			for day := range state.Days {
				if !keep[day] {
					delete(state.Days, day)
				}
			}
		}
		b, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if string(b) == raw || (len(state.Days) == 0 && raw == "") {
			return nil
		}
		return dnfrepo.SaveAccountMetadataEntry(ctx, accounts, a, LaborDayMetadataKey, string(b), cmd.Now)
	}, cmd.Validate)
	if err != nil {
		return LaborDaySnapshot{}, err
	}
	return result, nil
}
