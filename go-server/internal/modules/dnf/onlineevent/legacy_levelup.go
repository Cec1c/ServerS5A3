package onlineevent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const LegacyLevelPVFPath = "event/decidelevelupevent.evt"
const LegacyLevelMetadataKey = "event_legacy_levelup_116_v1"

type LegacyLevelReward struct {
	Level uint32
	Item  AttendancePVFItemReward
}

type LegacyLevelCatalog struct {
	Main  []LegacyLevelReward
	Stage []LegacyLevelReward
}

func LoadLegacyLevelCatalog(ctx context.Context, source dnfpvf.Source) (*LegacyLevelCatalog, error) {
	text, err := source.ReadText(LegacyLevelPVFPath)
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse(LegacyLevelPVFPath, text)
	if err != nil {
		return nil, err
	}
	catalog := &LegacyLevelCatalog{}
	all := []AttendancePVFItemReward{}
	for _, spec := range []struct {
		name  string
		count int
		dst   *[]LegacyLevelReward
	}{
		{"Main Reward", 6, &catalog.Main}, {"Sub Reward", 8, &catalog.Stage},
	} {
		values, err := attendanceIntegerSection(doc, spec.name, spec.count*4)
		if err != nil {
			return nil, err
		}
		var previous int64
		for i := 0; i < len(values); i += 4 {
			level, id, count, enabled := values[i], values[i+1], values[i+2], values[i+3]
			if level <= previous || level > 90 || id <= 0 || id > 0xffffffff || count <= 0 || count > 0x7fffffff || enabled != 1 {
				return nil, fmt.Errorf("legacy level reward [%s] row=%d is invalid", spec.name, i/4)
			}
			reward := AttendancePVFItemReward{ItemID: id, Count: count}
			*spec.dst = append(*spec.dst, LegacyLevelReward{Level: uint32(level), Item: reward})
			all = append(all, reward)
			previous = level
		}
	}
	paths, err := resolveAttendanceRewardDefinitions(ctx, source, all)
	if err != nil {
		return nil, err
	}
	for _, rows := range [][]LegacyLevelReward{catalog.Main, catalog.Stage} {
		for i := range rows {
			rows[i].Item.DefinitionPath = paths[rows[i].Item.ItemID]
		}
	}
	return catalog, nil
}

type LegacyLevelState struct {
	Target  uint32 `json:"target"`
	Start   uint32 `json:"start"`
	Stages  uint32 `json:"stages"`
	Claimed bool   `json:"claimed"`
}

type LegacyLevelResult struct {
	LegacyLevelState
	MailIDs []string
}

type LegacyLevelCommand struct {
	AccountID, CharacterID string
	Target                 uint32
	Claim                  bool
	Now                    time.Time
	Validate               func() bool
	Deliver                func(dnfrepo.MailboxRepository, AttendancePVFItemReward, string) (string, error)
}

// Reservation and mail receipts share the existing mailbox/account transaction.
func SettleLegacyLevel(ctx context.Context, repos dnfrepo.Group, catalog *LegacyLevelCatalog, cmd LegacyLevelCommand) (LegacyLevelResult, error) {
	if repos.MailboxAssets == nil || catalog == nil || len(catalog.Main) == 0 || cmd.AccountID == "" || cmd.CharacterID == "" || cmd.Now.IsZero() || cmd.Deliver == nil {
		return LegacyLevelResult{}, ErrOwnerUnavailable
	}
	var result LegacyLevelResult
	err := withinPortedEventAssets(ctx, repos, cmd.AccountID, cmd.CharacterID, func(
		accounts dnfrepo.AccountRepository, characters dnfrepo.CharacterRepository, _ dnfrepo.InventoryRepository, mailboxes dnfrepo.MailboxRepository,
	) error {
		character, found, err := characters.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || character.AccountID != cmd.AccountID || character.Level < 1 || character.Level > 90 {
			return fmt.Errorf("legacy level character ownership/level mismatch")
		}
		account, found, err := accounts.Load(ctx, cmd.AccountID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("legacy level account missing")
		}
		states := map[string]LegacyLevelState{}
		if raw := account.Metadata[LegacyLevelMetadataKey]; raw != "" {
			if err := json.Unmarshal([]byte(raw), &states); err != nil {
				return err
			}
			if states == nil {
				return fmt.Errorf("legacy level state is null")
			}
		}
		state := states[cmd.CharacterID]
		findMain := func(target uint32) (AttendancePVFItemReward, bool) {
			for _, row := range catalog.Main {
				if row.Level == target {
					return row.Item, true
				}
			}
			return AttendancePVFItemReward{}, false
		}
		main, valid := findMain(state.Target)
		if state.Target == 0 && state != (LegacyLevelState{}) {
			return fmt.Errorf("legacy level unreserved state is invalid")
		}
		if state.Target != 0 && (!valid || state.Start < 1 || state.Start >= state.Target || state.Stages>>len(catalog.Stage) != 0) {
			return fmt.Errorf("legacy level saved state is invalid")
		}
		changed := false
		if cmd.Target != 0 {
			if state.Target != 0 && state.Target != cmd.Target {
				return fmt.Errorf("legacy level already reserved")
			}
			if state.Target == 0 {
				main, valid = findMain(cmd.Target)
				if !valid || cmd.Target <= uint32(character.Level) {
					return fmt.Errorf("legacy level target is unavailable")
				}
				state.Target, state.Start = cmd.Target, uint32(character.Level)
				changed = true
			}
		}
		if cmd.Claim && (state.Target == 0 || uint32(character.Level) < state.Target) {
			return fmt.Errorf("legacy level target is not reached")
		}
		deliver := func(reward AttendancePVFItemReward, suffix string) error {
			key := LegacyLevelMetadataKey + ":" + cmd.CharacterID + ":" + suffix
			id, err := cmd.Deliver(mailboxes, reward, key)
			if err != nil {
				return err
			}
			if id == "" {
				return fmt.Errorf("legacy level reward mail missing")
			}
			result.MailIDs = append(result.MailIDs, id)
			changed = true
			return nil
		}
		if state.Target != 0 {
			for i, row := range catalog.Stage {
				if row.Level <= state.Start || row.Level > uint32(character.Level) || row.Level > state.Target || state.Stages&(1<<i) != 0 {
					continue
				}
				if err := deliver(row.Item, "stage:"+strconv.Itoa(int(row.Level))); err != nil {
					return err
				}
				state.Stages |= 1 << i
			}
			if cmd.Claim && !state.Claimed {
				if err := deliver(main, "main"); err != nil {
					return err
				}
				state.Claimed = true
			}
		}
		result.LegacyLevelState = state
		if !changed {
			return nil
		}
		states[cmd.CharacterID] = state
		raw, err := json.Marshal(states)
		if err != nil {
			return err
		}
		return dnfrepo.SaveAccountMetadataEntry(ctx, accounts, account, LegacyLevelMetadataKey, string(raw), cmd.Now)
	}, cmd.Validate)
	if err != nil {
		return LegacyLevelResult{}, err
	}
	return result, nil
}
