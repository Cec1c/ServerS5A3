package onlineevent

import (
	"context"
	"encoding/json"
	"fmt"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"time"
)

const OrdinaryPVFPath = "event/attendancedailyevent.evt"
const OrdinaryMetadataKey = "event_ordinary_attendance_v1"

type OrdinaryCatalog struct {
	MinimumLevel int
	Rewards      [28]AttendancePVFItemReward
}

func LoadOrdinaryCatalog(ctx context.Context, source dnfpvf.Source) (*OrdinaryCatalog, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if source == nil {
		return nil, ErrOwnerUnavailable
	}
	text, e := source.ReadText(OrdinaryPVFPath)
	if e != nil {
		return nil, e
	}
	doc, e := dnfpvf.Parse(OrdinaryPVFPath, text)
	if e != nil {
		return nil, e
	}
	c := &OrdinaryCatalog{}
	for _, spec := range []struct {
		name  string
		count int
	}{{"allow accumulate attendance", 1}, {"level limit", 1}, {"max attendance day", 1}, {"reward items", 84}} {
		v, e := attendanceIntegerSection(doc, spec.name, spec.count)
		if e != nil {
			return nil, e
		}
		switch spec.name {
		case "allow accumulate attendance":
			if v[0] != 0 {
				return nil, fmt.Errorf("ordinary accumulating policy unavailable")
			}
		case "level limit":
			if v[0] < 1 || v[0] > 100 {
				return nil, fmt.Errorf("ordinary level invalid")
			}
			c.MinimumLevel = int(v[0])
		case "max attendance day":
			if v[0] != 28 {
				return nil, fmt.Errorf("ordinary native28 catalog mismatch")
			}
		default:
			for i := 0; i < 28; i++ {
				if v[i*3] != int64(i+1) || v[i*3+1] <= 0 || v[i*3+1] > 0xffffffff || v[i*3+2] <= 0 || v[i*3+2] > 0xffffffff {
					return nil, fmt.Errorf("ordinary reward invalid")
				}
				c.Rewards[i] = AttendancePVFItemReward{ItemID: v[i*3+1], Count: v[i*3+2]}
			}
		}
	}
	paths, e := resolveAttendanceRewardDefinitions(ctx, source, c.Rewards[:])
	if e != nil {
		return nil, e
	}
	attachAttendanceDefinitionPaths(c.Rewards[:], paths)
	return c, nil
}

type ordinaryDay struct {
	Date    string `json:"date"`
	Claimed bool   `json:"claimed"`
}
type ordinaryState struct {
	LastObserved time.Time     `json:"last_observed"`
	Days         []ordinaryDay `json:"days"`
}

func loadOrdinaryState(a repo.AccountRecord) (ordinaryState, error) {
	var s ordinaryState
	if raw := a.Metadata[OrdinaryMetadataKey]; raw != "" {
		if e := json.Unmarshal([]byte(raw), &s); e != nil {
			return s, e
		}
	}
	if len(s.Days) > 28 {
		return s, fmt.Errorf("ordinary days over28")
	}
	previous := ""
	unclaimed := false
	for _, d := range s.Days {
		date, e := time.Parse("2006-01-02", d.Date)
		if e != nil || date.Format("2006-01-02") != d.Date || d.Date <= previous {
			return s, fmt.Errorf("ordinary day invalid")
		}
		if !d.Claimed {
			unclaimed = true
		} else if unclaimed {
			return s, fmt.Errorf("ordinary claim prefix invalid")
		}
		previous = d.Date
	}
	if len(s.Days) > 0 && (s.LastObserved.IsZero() || activityOrdinaryDate(s.LastObserved) < previous) {
		return s, fmt.Errorf("ordinary observation invalid")
	}
	return s, nil
}
func activityOrdinaryDate(now time.Time) string {
	return now.In(ChinaCalendar).Add(-6 * time.Hour).Format("2006-01-02")
}

type OrdinarySnapshot struct {
	Days, Claimed uint32
	CanClaim      bool
	MailIDs       []string
}

func ordinarySnapshot(s ordinaryState, eligible bool) OrdinarySnapshot {
	out := OrdinarySnapshot{Days: uint32(len(s.Days))}
	for _, d := range s.Days {
		if d.Claimed {
			out.Claimed++
		}
	}
	out.CanClaim = eligible && out.Claimed < out.Days
	return out
}

type OrdinaryCommand struct {
	AccountID, CharacterID string
	Now                    time.Time
	RegisterDay, Claim     bool
	Validate               func() bool
	Deliver                func(repo.MailboxRepository, AttendancePVFItemReward, string) (string, error)
}

// Account sharing and06:00 activity-day reset are the explicit new operating
// policy. Only actual eligible selected/login days exist; no historical backfill.
func SettleOrdinaryAttendance(ctx context.Context, repos repo.Group, c *OrdinaryCatalog, cmd OrdinaryCommand) (OrdinarySnapshot, error) {
	if c == nil || cmd.Now.IsZero() || cmd.AccountID == "" || cmd.CharacterID == "" || cmd.Deliver == nil {
		return OrdinarySnapshot{}, ErrOwnerUnavailable
	}
	u, ok := repos.MailboxAssets.(repo.AccountMailboxCharacterUnitOfWork)
	if !ok {
		return OrdinarySnapshot{}, ErrOwnerUnavailable
	}
	var result OrdinarySnapshot
	err := u.WithinAccountMailboxCharacter(ctx, cmd.AccountID, cmd.CharacterID, func(accounts repo.AccountRepository, ch repo.CharacterRecord, m repo.MailboxRepository) error {
		if cmd.Validate != nil && !cmd.Validate() {
			return ErrOwnerUnavailable
		}
		if ch.CharacterID != cmd.CharacterID || ch.AccountID != cmd.AccountID {
			return ErrOwnerUnavailable
		}
		a, found, e := accounts.Load(ctx, cmd.AccountID)
		if e != nil {
			return e
		}
		if !found || a.AccountID != cmd.AccountID {
			return ErrOwnerUnavailable
		}
		state, e := loadOrdinaryState(a)
		if e != nil {
			return e
		}
		if !state.LastObserved.IsZero() && cmd.Now.Before(state.LastObserved) {
			return ErrOwnerUnavailable
		}
		eligible := ch.Level >= c.MinimumLevel
		date := activityOrdinaryDate(cmd.Now)
		changed := false
		if cmd.RegisterDay && eligible && len(state.Days) < 28 && (len(state.Days) == 0 || state.Days[len(state.Days)-1].Date < date) {
			state.Days = append(state.Days, ordinaryDay{Date: date})
			changed = true
		}
		var ids []string
		if cmd.Claim && eligible {
			for i := range state.Days {
				if state.Days[i].Claimed {
					continue
				}
				id, e := cmd.Deliver(m, c.Rewards[i], OrdinaryMetadataKey+":"+state.Days[i].Date)
				if e != nil {
					return e
				}
				if id == "" {
					return fmt.Errorf("ordinary mail missing")
				}
				ids = append(ids, id)
				state.Days[i].Claimed = true
				changed = true
			}
		}
		result = ordinarySnapshot(state, eligible)
		result.MailIDs = ids
		if !changed {
			return nil
		}
		if cmd.Validate != nil && !cmd.Validate() {
			return ErrOwnerUnavailable
		}
		state.LastObserved = cmd.Now.UTC()
		raw, e := json.Marshal(state)
		if e != nil {
			return e
		}
		if e := repo.SaveAccountMetadataEntry(ctx, accounts, a, OrdinaryMetadataKey, string(raw), cmd.Now); e != nil {
			return e
		}
		if cmd.Validate != nil && !cmd.Validate() {
			return ErrOwnerUnavailable
		}
		return nil
	})
	if err != nil {
		return OrdinarySnapshot{}, err
	}
	return result, nil
}
