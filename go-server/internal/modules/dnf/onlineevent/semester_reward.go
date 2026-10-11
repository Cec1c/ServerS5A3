package onlineevent

import (
	"context"
	"encoding/binary"
	"fmt"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"math/bits"
	"strconv"
	"time"
)

const SemesterPVFPath = "event/newsemesterattendanceevent.evt"

type SemesterCatalog struct{ Rewards [3]AttendancePVFItemReward }

func LoadSemesterCatalog(ctx context.Context, source pvf.Source) (*SemesterCatalog, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if source == nil {
		return nil, ErrOwnerUnavailable
	}
	text, e := source.ReadText(SemesterPVFPath)
	if e != nil {
		return nil, e
	}
	doc, e := pvf.Parse(SemesterPVFPath, text)
	if e != nil {
		return nil, e
	}
	control, ok := doc.Section("control panel")
	if !ok || len(control) != 4 || control[0].Kind != pvf.TokenInt || control[0].Int != 1 || control[1].Kind != pvf.TokenInt || control[1].Int != 7 || control[2].Kind != pvf.TokenInt || control[2].Int != 28 || control[3].Kind != pvf.TokenString || control[3].Value != "event_1511_attendance_event" {
		return nil, fmt.Errorf("semester control panel contract")
	}
	for _, s := range []struct {
		name string
		want []int64
	}{{"reward info", []int64{0}}, {"special dungeon", []int64{1, 6062}}, {"accu attendance days", []int64{3, 8, 490010219, 1, 16, 490010217, 1, 24, 490010215, 1}}} {
		v, e := attendanceIntegerSection(doc, s.name, len(s.want))
		if e != nil {
			return nil, e
		}
		for i, n := range v {
			if n != s.want[i] {
				return nil, fmt.Errorf("semester exact %s catalog", s.name)
			}
		}
	}
	c := &SemesterCatalog{Rewards: [3]AttendancePVFItemReward{{ItemID: 490010219, Count: 1}, {ItemID: 490010217, Count: 1}, {ItemID: 490010215, Count: 1}}}
	paths, e := resolveAttendanceRewardDefinitions(ctx, source, c.Rewards[:])
	if e != nil {
		return nil, e
	}
	attachAttendanceDefinitionPaths(c.Rewards[:], paths)
	return c, nil
}

func SemesterJournalKey(c EpicCampaign) string {
	return "event_new_semester_v1:" + c.Starts.UTC().Format("20060102T150405Z")
}

type SemesterSnapshot struct {
	Qualified, Processed, Today uint8
	Eligible, Claimed           uint32
	MailIDs                     []string
}

func SemesterCharacterSnapshot(ch repo.CharacterRecord, c EpicCampaign, now time.Time) (SemesterSnapshot, error) {
	if !c.Active(now) || ch.CharacterID == "" || ch.AccountID == "" || ch.Level < 17 {
		return SemesterSnapshot{}, ErrOwnerUnavailable
	}
	k := SemesterJournalKey(c)
	mask := ch.Stats[k+":day_bitmap"]
	n := ch.Stats[k+":qualified_days"]
	version := ch.Stats[k+":version"]
	claimed := ch.Stats[k+":claim_bitmap"]
	observed := ch.Stats[k+":claim_observed"]
	if mask < 0 || mask >= 1<<28 || claimed < 0 || claimed > 7 || observed < 0 || observed > now.Unix() || n != int64(bits.OnesCount32(uint32(mask))) || (mask != 0 && version != 1) || (version != 0 && version != 1) {
		return SemesterSnapshot{}, fmt.Errorf("semester durable state invalid")
	}
	day := now.In(ChinaCalendar).Add(-6 * time.Hour)
	start := c.Starts.In(ChinaCalendar).Add(-6 * time.Hour)
	ordinal := int(time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, ChinaCalendar).Sub(time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, ChinaCalendar)) / (24 * time.Hour))
	if ordinal < 0 || ordinal >= 28 || uint32(mask)>>uint(ordinal+1) != 0 {
		return SemesterSnapshot{}, fmt.Errorf("semester future journal bit")
	}
	out := SemesterSnapshot{Qualified: uint8(n), Processed: uint8(n), Claimed: uint32(claimed)}
	// Local presentation policy: empty daily reward table has no claim ledger.
	// Processed equals Qualified solely to suppress unsupported daily grants.
	if uint32(mask)&(1<<uint(ordinal)) != 0 {
		out.Today = 1
	}
	for i, t := range []int64{8, 16, 24} {
		if n >= t {
			out.Eligible |= 1 << uint(i)
		}
	}
	if out.Claimed&^out.Eligible != 0 {
		return SemesterSnapshot{}, fmt.Errorf("semester impossible claimed bit")
	}
	return out, nil
}

// Pure NOTI654 body builder; deliberately not wired to a bridge or timer.
func BuildSemesterNotification(s SemesterSnapshot) ([]byte, error) {
	if s.Qualified > 28 || s.Processed != s.Qualified || s.Today > 1 || s.Eligible > 7 || s.Claimed&^s.Eligible != 0 {
		return nil, ErrOwnerUnavailable
	}
	var eligible uint32
	for i, t := range []uint8{8, 16, 24} {
		if s.Qualified >= t {
			eligible |= 1 << uint(i)
		}
	}
	if s.Eligible != eligible {
		return nil, ErrOwnerUnavailable
	}
	b := make([]byte, 11)
	b[0] = s.Qualified
	b[1] = s.Processed
	b[2] = s.Today
	binary.LittleEndian.PutUint32(b[3:], s.Eligible)
	binary.LittleEndian.PutUint32(b[7:], s.Claimed)
	return b, nil
}

type SemesterClaimCommand struct {
	AccountID, CharacterID string
	Campaign               EpicCampaign
	Now                    time.Time
	Index                  uint32
	Validate               func() bool
	Deliver                func(repo.MailboxRepository, AttendancePVFItemReward, string) (string, error)
}

func ClaimSemesterReward(ctx context.Context, g repo.Group, c *SemesterCatalog, cmd SemesterClaimCommand) (SemesterSnapshot, error) {
	if g.MailboxAssets == nil || c == nil || cmd.AccountID == "" || cmd.CharacterID == "" || cmd.Index >= 3 || !cmd.Campaign.Active(cmd.Now) || cmd.Validate == nil || cmd.Deliver == nil {
		return SemesterSnapshot{}, ErrOwnerUnavailable
	}
	for i, id := range []int64{490010219, 490010217, 490010215} {
		if c.Rewards[i].ItemID != id || c.Rewards[i].Count != 1 {
			return SemesterSnapshot{}, ErrOwnerUnavailable
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var out SemesterSnapshot
	e := g.MailboxAssets.WithinMailboxAssets(ctx, cmd.CharacterID, cmd.CharacterID, func(chars repo.CharacterRepository, _ repo.InventoryRepository, mail repo.MailboxRepository) (err error) {
		defer func() {
			if v := recover(); v != nil {
				err = fmt.Errorf("semester callback panic: %v", v)
			}
		}()
		if e := ctx.Err(); e != nil {
			return e
		}
		if !cmd.Validate() {
			return ErrOwnerUnavailable
		}
		ch, found, e := chars.Load(ctx, cmd.CharacterID)
		if e != nil {
			return e
		}
		if !found || ch.CharacterID != cmd.CharacterID || ch.AccountID != cmd.AccountID {
			return ErrOwnerUnavailable
		}
		ch = repo.CloneCharacter(ch)
		state, e := SemesterCharacterSnapshot(ch, cmd.Campaign, cmd.Now)
		if e != nil {
			return e
		}
		bit := uint32(1) << cmd.Index
		if state.Claimed&bit != 0 {
			out = state
			return nil
		}
		if state.Eligible&bit == 0 {
			return ErrOwnerUnavailable
		}
		key := SemesterJournalKey(cmd.Campaign)
		id, e := cmd.Deliver(mail, c.Rewards[cmd.Index], key+":"+cmd.AccountID+":"+cmd.CharacterID+":"+strconv.Itoa(int(cmd.Index)))
		if e != nil {
			return e
		}
		if id == "" {
			return fmt.Errorf("semester mail missing")
		}
		if ch.Stats == nil {
			ch.Stats = map[string]int64{}
		}
		state.Claimed |= bit
		ch.Stats[key+":claim_bitmap"] = int64(state.Claimed)
		ch.Stats[key+":claim_observed"] = cmd.Now.Unix()
		ch.UpdatedAt = cmd.Now
		if e := repo.SaveCharacterFields(ctx, chars, ch, repo.CharacterFieldStats); e != nil {
			return e
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		if !cmd.Validate() {
			return ErrOwnerUnavailable
		}
		state.MailIDs = []string{id}
		out = state
		return nil
	})
	if e != nil {
		return SemesterSnapshot{}, e
	}
	return out, nil
}
