package raid

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrWeeklyClearLimit = errors.New("raid weekly clear limit reached")
var ErrWeeklyClearState = errors.New("raid weekly clear state unavailable or inconsistent")

// WeeklyPolicy is explicit deployment policy. Neither packet1442 nor the PVF
// WEEKLY CLEAR COUNT field establishes a weekday; callers must supply it.
type WeeklyPolicy struct {
	ResetWeekday time.Weekday
	ResetHour    int
}

func WeeklyEpoch(now time.Time, policy WeeklyPolicy) (int64, error) {
	if now.IsZero() || policy.ResetWeekday < time.Sunday || policy.ResetWeekday > time.Saturday || policy.ResetHour < 0 || policy.ResetHour > 23 {
		return 0, ErrWeeklyClearState
	}
	local := now.In(time.FixedZone("DNF service UTC+8", 8*60*60))
	reset := time.Date(local.Year(), local.Month(), local.Day(), policy.ResetHour, 0, 0, 0, local.Location())
	if local.Before(reset) {
		reset = reset.AddDate(0, 0, -1)
	}
	back := (int(reset.Weekday()) - int(policy.ResetWeekday) + 7) % 7
	return reset.AddDate(0, 0, -back).Unix(), nil
}

func weeklyKeys(mode byte) (string, string, error) {
	if mode < 1 || mode > 3 {
		return "", "", ErrWeeklyClearState
	}
	suffix := strconv.Itoa(int(mode))
	return "raid_weekly_epoch_v1_" + suffix, "raid_weekly_clears_v1_" + suffix, nil
}

// Missing v1 counters mean no clears recorded by this owner, not recovered
// legacy history. Callers must document the tracking baseline at deployment.
func WeeklyClears(character repo.CharacterRecord, mode byte, now time.Time, policy WeeklyPolicy) (int, error) {
	epochKey, countKey, err := weeklyKeys(mode)
	if err != nil {
		return 0, err
	}
	epoch, err := WeeklyEpoch(now, policy)
	if err != nil {
		return 0, err
	}
	stored, hasEpoch := character.Stats[epochKey]
	used, hasCount := character.Stats[countKey]
	if !hasEpoch && !hasCount {
		return 0, nil
	}
	if hasEpoch != hasCount || stored <= 0 || stored > epoch || used < 0 || used > 255 {
		return 0, ErrWeeklyClearState
	}
	if stored < epoch {
		return 0, nil
	}
	return int(used), nil
}

type ClearMember struct {
	CharacterID string
	AccountID   string
}
type WeeklyClearCommand struct {
	Mode        byte
	Limit       int
	InstanceID  string
	Members     []ClearMember
	CompletedAt time.Time
	Policy      WeeklyPolicy
}

type WeeklyClearResult struct {
	Used     map[string]int
	Replayed bool
}

// CommitWeeklyClear is called only by an authoritative completed raid. The
// entire raid group commits once; failure of any member rolls back all counts.
// A per-instance marker survives restarts and later weeks to reject old replay.
func CommitWeeklyClear(ctx context.Context, group repo.Group, cmd WeeklyClearCommand) (WeeklyClearResult, error) {
	unit, ok := group.CharacterTrade.(repo.CharacterInventoryGroupUnitOfWork)
	instance, decodeErr := hex.DecodeString(cmd.InstanceID)
	if !ok || decodeErr != nil || len(instance) != 16 || cmd.Limit < 1 || cmd.Limit > 255 || len(cmd.Members) < 1 || len(cmd.Members) > MaxAttackPartyMembers {
		return WeeklyClearResult{}, ErrWeeklyClearState
	}
	epochKey, countKey, err := weeklyKeys(cmd.Mode)
	if err != nil {
		return WeeklyClearResult{}, err
	}
	epoch, err := WeeklyEpoch(cmd.CompletedAt, cmd.Policy)
	if err != nil {
		return WeeklyClearResult{}, err
	}
	ids := make([]string, len(cmd.Members))
	members := make(map[string]ClearMember, len(cmd.Members))
	for i, member := range cmd.Members {
		if member.CharacterID == "" || member.AccountID == "" {
			return WeeklyClearResult{}, ErrWeeklyClearState
		}
		ids[i] = member.CharacterID
		members[member.CharacterID] = member
	}
	ids, err = repo.CharacterInventoryGroupIDs(ids)
	if err != nil {
		return WeeklyClearResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	marker := fmt.Sprintf("raid_clear_v1_%d_%s", cmd.Mode, hex.EncodeToString(instance))
	result := WeeklyClearResult{Used: map[string]int{}}
	err = unit.WithinCharacterInventoryGroup(ctx, ids, func(chars repo.CharacterRepository, _ repo.InventoryRepository) error {
		characters := make([]repo.CharacterRecord, 0, len(ids))
		marked := 0
		for _, id := range ids {
			character, found, err := chars.Load(ctx, id)
			if err != nil {
				return err
			}
			if !found || character.AccountID != members[id].AccountID {
				return ErrWeeklyClearState
			}
			used, err := WeeklyClears(character, cmd.Mode, cmd.CompletedAt, cmd.Policy)
			if err != nil {
				return err
			}
			if stamp, exists := character.Stats[marker]; exists {
				if stamp <= 0 || stamp > epoch {
					return ErrWeeklyClearState
				}
				marked++
			}
			result.Used[id] = used
			characters = append(characters, repo.CloneCharacter(character))
		}
		if marked == len(ids) {
			result.Replayed = true
			return nil
		}
		if marked != 0 {
			return ErrWeeklyClearState
		}
		for _, character := range characters {
			if result.Used[character.CharacterID] >= cmd.Limit {
				return ErrWeeklyClearLimit
			}
		}
		for _, character := range characters {
			if character.Stats == nil {
				character.Stats = map[string]int64{}
			}
			used := result.Used[character.CharacterID] + 1
			character.Stats[epochKey] = epoch
			character.Stats[countKey] = int64(used)
			character.Stats[marker] = epoch
			character.UpdatedAt = cmd.CompletedAt
			if err := chars.Save(ctx, character); err != nil {
				return err
			}
			result.Used[character.CharacterID] = used
		}
		return nil
	})
	if err != nil {
		return WeeklyClearResult{}, err
	}
	return result, nil
}

// Native1442 is one count byte then pairs of mode/used bytes. Limits are read
// from actual client PVF; this packet must carry the recorded used count.
func BuildWeeklyClearBody(used map[byte]int) ([]byte, error) {
	if len(used) != 3 {
		return nil, ErrWeeklyClearState
	}
	var writer packetWriter
	writer.writeByte(3)
	for mode := byte(1); mode <= 3; mode++ {
		count, exists := used[mode]
		if !exists || count < 0 || count > 255 {
			return nil, ErrWeeklyClearState
		}
		// Current menu 0133121D/013312AB/0133134B requests native
		// keys 0/1/2 for Anton/Luke/FiendWar. Persistent domain modes stay
		// 1/2/3; convert only at this protocol boundary.
		writer.writeByte(mode - 1)
		writer.writeByte(byte(count))
	}
	return writer.bytes(), nil
}
