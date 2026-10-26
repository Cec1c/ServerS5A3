package onlineevent

import (
	"context"
	"fmt"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"strconv"
	"time"
)

const EpicPVFPath = "event/hellpartydungeonevent.evt"

type EpicCatalog struct {
	Thresholds        [3]uint32
	Rewards           [3]AttendancePVFItemReward
	EntryAlternatives map[int64][][]AttendancePVFItemReward
}

func LoadEpicCatalog(ctx context.Context, source pvf.Source) (*EpicCatalog, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if source == nil {
		return nil, ErrOwnerUnavailable
	}
	text, e := source.ReadText(EpicPVFPath)
	if e != nil {
		return nil, e
	}
	doc, e := pvf.Parse(EpicPVFPath, text)
	if e != nil {
		return nil, e
	}
	required, e := attendanceIntegerSection(doc, "dungeon required items", 42)
	if e != nil {
		return nil, e
	}
	want := [42]int64{1, 6063, 490010214, 1, 3330, 0, 0, 6063, 490010214, 0, 3330, 30, 1, 6064, 490010214, 1, 3330, 15, 0, 6064, 490010214, 0, 3330, 45, 2, 6065, 490010214, 2, 3330, 0, 1, 6065, 490010214, 1, 3330, 30, 0, 6065, 490010214, 0, 3330, 60}
	for i, v := range required {
		if v != want[i] {
			return nil, fmt.Errorf("epic exact entry alternatives catalog mismatch")
		}
	}
	if _, e = source.ReadText("ui/event/hellpartydungeonevent/hellpartydungeonevent.xui"); e != nil {
		return nil, e
	}
	tokens, ok := doc.Section("accu reward items")
	if !ok || len(tokens) != 15 {
		return nil, fmt.Errorf("epic reward tuple count")
	}
	c := &EpicCatalog{EntryAlternatives: make(map[int64][][]AttendancePVFItemReward)}
	for i := 0; i < len(required); i += 6 {
		var items []AttendancePVFItemReward
		for j := 2; j < 6; j += 2 {
			if required[i+j+1] > 0 {
				items = append(items, AttendancePVFItemReward{ItemID: required[i+j], Count: required[i+j+1]})
			}
		}
		id := required[i+1]
		c.EntryAlternatives[id] = append(c.EntryAlternatives[id], items)
	}
	for i := 0; i < 3; i++ {
		row := tokens[i*5 : i*5+5]
		for j := 0; j < 3; j++ {
			if row[j].Kind != pvf.TokenInt || row[j].Int <= 0 || row[j].Int > math.MaxUint32 {
				return nil, fmt.Errorf("epic reward integer")
			}
		}
		if row[3].Kind != pvf.TokenString || row[4].Kind != pvf.TokenString {
			return nil, fmt.Errorf("epic reward mail resource")
		}
		c.Thresholds[i] = uint32(row[0].Int)
		c.Rewards[i] = AttendancePVFItemReward{ItemID: row[1].Int, Count: row[2].Int}
	}
	if c.Thresholds != [3]uint32{1800, 9000, 18000} {
		return nil, fmt.Errorf("epic current threshold contract")
	}
	paths, e := resolveAttendanceRewardDefinitions(ctx, source, c.Rewards[:])
	if e != nil {
		return nil, e
	}
	attachAttendanceDefinitionPaths(c.Rewards[:], paths)
	return c, nil
}

type EpicSnapshot struct {
	Invitations uint32
	Claimed     uint32
	MailIDs     []string
}

func epicStatKey(c EpicCampaign, field string) string { return c.Key() + ":" + field }
func EpicCharacterSnapshot(ch repo.CharacterRecord, c EpicCampaign, now time.Time) (EpicSnapshot, error) {
	if c.Validate() != nil || now.IsZero() {
		return EpicSnapshot{}, ErrOwnerUnavailable
	}
	n, m, last := ch.Stats[epicStatKey(c, "invitations")], ch.Stats[epicStatKey(c, "claimed")], ch.Stats[epicStatKey(c, "observed")]
	if n < 0 || n > math.MaxUint32 || m < 0 || m > 7 || last < 0 || last > now.Unix() {
		return EpicSnapshot{}, fmt.Errorf("epic persisted state invalid or clock rollback")
	}
	thresholds := [3]int64{1800, 9000, 18000}
	for i, t := range thresholds {
		if m&(1<<i) != 0 && n < t {
			return EpicSnapshot{}, fmt.Errorf("epic impossible claim mask")
		}
	}
	return EpicSnapshot{Invitations: uint32(n), Claimed: uint32(m)}, nil
}

// Caller owns the physical CharacterAssets transaction. Count actual item3330
// consumption once, with its entry receipt; token substitutes are not counted.
func AccumulateEpicInvitations(ch *repo.CharacterRecord, c EpicCampaign, now time.Time, count int64) error {
	if ch == nil || count <= 0 {
		return ErrOwnerUnavailable
	}
	state, e := EpicCharacterSnapshot(*ch, c, now)
	if e != nil {
		return e
	}
	if !c.Active(now) {
		return nil
	}
	if int64(state.Invitations) > math.MaxUint32-count {
		return fmt.Errorf("epic counter overflow")
	}
	if ch.Stats == nil {
		ch.Stats = map[string]int64{}
	}
	ch.Stats[epicStatKey(c, "invitations")] = int64(state.Invitations) + count
	ch.Stats[epicStatKey(c, "observed")] = now.Unix()
	return nil
}

type EpicClaimCommand struct {
	AccountID, CharacterID string
	Campaign               EpicCampaign
	Now                    time.Time
	Index                  uint32
	Validate               func() bool
	Deliver                func(repo.MailboxRepository, AttendancePVFItemReward, string) (string, error)
}

func ClaimEpicReward(ctx context.Context, repos repo.Group, catalog *EpicCatalog, cmd EpicClaimCommand) (EpicSnapshot, error) {
	if repos.MailboxAssets == nil || catalog == nil || catalog.Thresholds != [3]uint32{1800, 9000, 18000} || cmd.AccountID == "" || cmd.CharacterID == "" || cmd.Index >= 3 || !cmd.Campaign.Active(cmd.Now) || cmd.Deliver == nil || cmd.Validate == nil {
		return EpicSnapshot{}, ErrOwnerUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var out EpicSnapshot
	e := repos.MailboxAssets.WithinMailboxAssets(ctx, cmd.CharacterID, cmd.CharacterID, func(chars repo.CharacterRepository, _ repo.InventoryRepository, m repo.MailboxRepository) (callbackErr error) {
		defer func() {
			if v := recover(); v != nil {
				callbackErr = fmt.Errorf("epic claim callback panic: %v", v)
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
		if !found || ch.CharacterID != cmd.CharacterID || ch.AccountID != cmd.AccountID || ch.Level < 85 {
			return ErrOwnerUnavailable
		}
		ch = repo.CloneCharacter(ch)
		state, e := EpicCharacterSnapshot(ch, cmd.Campaign, cmd.Now)
		if e != nil {
			return e
		}
		bit := uint32(1) << cmd.Index
		if state.Claimed&bit != 0 {
			out = state
			return nil
		}
		if state.Invitations < catalog.Thresholds[cmd.Index] {
			return fmt.Errorf("epic claim threshold not reached")
		}
		id, e := cmd.Deliver(m, catalog.Rewards[cmd.Index], cmd.Campaign.Key()+":"+cmd.AccountID+":"+cmd.CharacterID+":"+strconv.Itoa(int(cmd.Index)))
		if e != nil {
			return e
		}
		if id == "" {
			return fmt.Errorf("epic reward mail missing")
		}
		if ch.Stats == nil {
			ch.Stats = map[string]int64{}
		}
		state.Claimed |= bit
		ch.Stats[epicStatKey(cmd.Campaign, "claimed")] = int64(state.Claimed)
		ch.Stats[epicStatKey(cmd.Campaign, "observed")] = cmd.Now.Unix()
		ch.UpdatedAt = cmd.Now
		if e = repo.SaveCharacterFields(ctx, chars, ch, repo.CharacterFieldStats); e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
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
		return EpicSnapshot{}, e
	}
	return out, nil
}
