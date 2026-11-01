package onlineevent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/jobmap"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const LevelAimingPVFPath = "event/levelaimingevent.evt"
const LevelAimingMetadataKey = "event_level_aiming_222_v1"
const LevelAimingRoleStat = "level_aiming_222_role"

// These are the ten targets in the current EXE's dword_435A72C.
var LevelAimingTargets = [...]uint32{20, 30, 40, 50, 60, 70, 75, 80, 85, 86}

type LevelAimingCatalog struct {
	Limit, MinLevel, MaxLevel, Job int
	Starts, Ends                   time.Time
	Main                           AttendancePVFItemReward
	Points                         map[uint32]int64
	Stages                         []LegacyLevelReward
}

func LoadLevelAimingCatalog(ctx context.Context, source dnfpvf.Source) (*LevelAimingCatalog, error) {
	text, err := source.ReadText(LevelAimingPVFPath)
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse(LevelAimingPVFPath, text)
	if err != nil {
		return nil, err
	}
	control, ok := doc.Section("control panel")
	if !ok || len(control) != 10 {
		return nil, fmt.Errorf("level aiming control panel requires 10 values")
	}
	for i := 0; i < 5; i++ {
		if control[i].Kind != dnfpvf.TokenInt {
			return nil, fmt.Errorf("level aiming control[%d] is not an integer", i)
		}
	}
	c := &LevelAimingCatalog{Limit: int(control[1].Int), MinLevel: int(control[2].Int), MaxLevel: int(control[3].Int), Job: int(control[4].Int), Points: map[uint32]int64{}}
	if control[0].Int != 2 || c.Limit < 1 || c.Limit > 5 || c.MinLevel != 1 || c.MaxLevel != 86 || (c.Job != 99 && !jobmap.Valid(c.Job)) {
		return nil, fmt.Errorf("unsupported level aiming control panel")
	}
	loc := time.FixedZone("CST", 8*3600)
	c.Starts, err = time.ParseInLocation("2006-01-02 15:04:05", control[5].Value, loc)
	if err != nil {
		return nil, err
	}
	c.Ends, err = time.ParseInLocation("2006-01-02 15:04:05", control[6].Value, loc)
	if err != nil || !c.Ends.After(c.Starts) {
		return nil, fmt.Errorf("invalid level aiming dates")
	}
	// The currently deployed new-character reward ratio is exactly one.
	if control[5].Kind != dnfpvf.TokenString || control[6].Kind != dnfpvf.TokenString || control[9].Kind != dnfpvf.TokenString || control[7].Float != 1 || control[8].Float != 1 {
		return nil, fmt.Errorf("unsupported level aiming control types or reward ratio")
	}
	reward, err := attendanceIntegerSection(doc, "reward", 3)
	if err != nil {
		return nil, err
	}
	if reward[0] != 0 || reward[1] <= 0 || reward[1] > 0xffffffff || reward[2] != 1 {
		return nil, fmt.Errorf("invalid level aiming main reward")
	}
	c.Main = AttendancePVFItemReward{ItemID: reward[1], Count: reward[2]}
	points, err := attendanceIntegerSection(doc, "main reward point table", 180)
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(points); i += 2 {
		if points[i] != int64(i/2+1) || points[i+1] < 0 || points[i+1] > 0x7fffffff {
			return nil, fmt.Errorf("invalid level aiming point row")
		}
		c.Points[uint32(points[i])] = points[i+1]
	}
	stages, err := attendanceIntegerSection(doc, "sub reward lv table", 48)
	if err != nil {
		return nil, err
	}
	all := []AttendancePVFItemReward{c.Main}
	for i := 0; i < len(stages); i += 3 {
		if stages[i] < 1 || stages[i] > int64(c.MaxLevel) || (i > 0 && stages[i] <= stages[i-3]) || stages[i+1] <= 0 || stages[i+1] > 0xffffffff || stages[i+2] <= 0 || stages[i+2] > 0x7fffffff {
			return nil, fmt.Errorf("invalid level aiming stage row")
		}
		r := AttendancePVFItemReward{ItemID: stages[i+1], Count: stages[i+2]}
		c.Stages = append(c.Stages, LegacyLevelReward{Level: uint32(stages[i]), Item: r})
		all = append(all, r)
	}
	paths, err := resolveAttendanceRewardDefinitions(ctx, source, all)
	if err != nil {
		return nil, err
	}
	c.Main.DefinitionPath = paths[c.Main.ItemID]
	for i := range c.Stages {
		c.Stages[i].Item.DefinitionPath = paths[c.Stages[i].Item.ItemID]
	}
	return c, nil
}

type LevelAimingState struct {
	CharacterID                 string `json:"character_id"`
	Start, Target, Reservations uint32
	Stages                      uint32
	Awarded                     bool
}

func ReadLevelAimingState(account dnfrepo.AccountRecord) (LevelAimingState, error) {
	var state LevelAimingState
	if raw := account.Metadata[LevelAimingMetadataKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return state, err
		}
		id, err := strconv.ParseUint(state.CharacterID, 10, 16)
		if err != nil || id == 0 || state.Start < 1 || state.Start > 86 || state.Reservations > 5 || state.Stages>>16 != 0 || (state.Target == 0 && (state.Start != 1 || state.Reservations != 0 || state.Awarded || state.Stages != 0)) || (state.Target != 0 && (!slices.Contains(LevelAimingTargets[:], state.Target) || state.Reservations == 0 || state.Start > state.Target)) {
			return state, fmt.Errorf("invalid saved level aiming state")
		}
		if (state.Reservations <= 1 && state.Start != 1) || (state.Reservations > 1 && !slices.Contains(LevelAimingTargets[:], state.Start-1)) {
			return state, fmt.Errorf("invalid saved level aiming interval")
		}
	}
	return state, nil
}

type LevelAimingCommand struct {
	AccountID, CharacterID string
	Register               bool
	Target                 uint32
	Now                    time.Time
	Validate               func() bool
	Deliver                func(dnfrepo.MailboxRepository, AttendancePVFItemReward, string) (string, error)
}

type LevelAimingResult struct {
	LevelAimingState
	Character dnfrepo.CharacterRecord
	MailIDs   []string
}

func SettleLevelAiming(ctx context.Context, repos dnfrepo.Group, c *LevelAimingCatalog, cmd LevelAimingCommand) (LevelAimingResult, error) {
	if c == nil || repos.MailboxAssets == nil || cmd.AccountID == "" || cmd.CharacterID == "" || cmd.Now.IsZero() {
		return LevelAimingResult{}, ErrOwnerUnavailable
	}
	var result LevelAimingResult
	err := withinPortedEventAssets(ctx, repos, cmd.AccountID, cmd.CharacterID, func(accounts dnfrepo.AccountRepository, characters dnfrepo.CharacterRepository, _ dnfrepo.InventoryRepository, mailboxes dnfrepo.MailboxRepository) error {
		ch, found, err := characters.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || ch.AccountID != cmd.AccountID {
			return fmt.Errorf("level aiming character ownership mismatch")
		}
		account, found, err := accounts.Load(ctx, cmd.AccountID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("level aiming account missing")
		}
		state, err := ReadLevelAimingState(account)
		if err != nil {
			return err
		}
		result.Character = ch
		result.LevelAimingState = state
		active := (ActivityWindow{Starts: c.Starts, Ends: c.Ends}).ActiveAt(cmd.Now)
		if (cmd.Register || cmd.Target != 0) && !active {
			return fmt.Errorf("level aiming event is outside its dates")
		}
		changed := false
		if cmd.Register && state.CharacterID == "" {
			job, err := strconv.Atoi(ch.Job)
			if err != nil || !jobmap.Valid(job) || (c.Job != 99 && c.Job != job) || ch.Level != 1 || !(ActivityWindow{Starts: c.Starts, Ends: c.Ends}).ActiveAt(ch.CreatedAt) {
				return fmt.Errorf("level aiming requires a new eligible level-one character")
			}
			state = LevelAimingState{CharacterID: ch.CharacterID, Start: 1}
			changed = true
		}
		if (cmd.Register || cmd.Target != 0) && state.CharacterID != ch.CharacterID {
			return fmt.Errorf("level aiming account already owns another event character")
		}
		if state.CharacterID != ch.CharacterID || !active {
			return nil
		}
		deliver := func(reward AttendancePVFItemReward, key string) error {
			if cmd.Deliver == nil {
				return ErrOwnerUnavailable
			}
			id, err := cmd.Deliver(mailboxes, reward, LevelAimingMetadataKey+":"+ch.CharacterID+":"+key)
			if err != nil {
				return err
			}
			if id == "" {
				return fmt.Errorf("level aiming reward mail missing")
			}
			result.MailIDs = append(result.MailIDs, id)
			changed = true
			return nil
		}
		// Settle the preceding target before accepting the next reservation.
		if state.Target != 0 && uint32(ch.Level) >= state.Target && !state.Awarded {
			var count int64
			for level := state.Start; level <= state.Target; level++ {
				count += c.Points[level]
			}
			if count > 0 {
				reward := c.Main
				if reward.Count <= 0 || count > 0x7fffffff/reward.Count {
					return fmt.Errorf("level aiming reward count overflow")
				}
				reward.Count *= count
				if err := deliver(reward, "target:"+strconv.Itoa(int(state.Target))); err != nil {
					return err
				}
			}
			state.Awarded = true
			changed = true
		}
		if cmd.Target != 0 && cmd.Target != state.Target {
			if !slices.Contains(LevelAimingTargets[:], cmd.Target) || cmd.Target <= uint32(ch.Level) || cmd.Target <= state.Target || state.Reservations >= uint32(c.Limit) || (state.Target != 0 && !state.Awarded) {
				return fmt.Errorf("level aiming target is unavailable")
			}
			start := uint32(1)
			if state.Target != 0 {
				start = state.Target + 1
			}
			state.Start, state.Target, state.Awarded = start, cmd.Target, false
			state.Reservations++
			changed = true
		}
		if state.Target != 0 {
			for i, row := range c.Stages {
				if row.Level > uint32(ch.Level) || state.Stages&(1<<i) != 0 {
					continue
				}
				if err := deliver(row.Item, "stage:"+strconv.Itoa(int(row.Level))); err != nil {
					return err
				}
				state.Stages |= 1 << i
			}
		}
		if changed {
			ch = dnfrepo.CloneCharacter(ch)
			if ch.Stats == nil {
				ch.Stats = map[string]int64{}
			}
			role := int64(3)
			if state.Target == 0 {
				role = 5
			}
			ch.Stats[LevelAimingRoleStat] = role
			ch.UpdatedAt = cmd.Now
			if err := dnfrepo.SaveCharacterFields(ctx, characters, ch, dnfrepo.CharacterFieldStats); err != nil {
				return err
			}
			raw, err := json.Marshal(state)
			if err != nil {
				return err
			}
			if err := dnfrepo.SaveAccountMetadataEntry(ctx, accounts, account, LevelAimingMetadataKey, string(raw), cmd.Now); err != nil {
				return err
			}
		}
		result.LevelAimingState, result.Character = state, ch
		return nil
	}, cmd.Validate)
	if err != nil {
		return LevelAimingResult{}, err
	}
	return result, nil
}
