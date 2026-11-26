// Package weaponskin owns weapon appearance registration and application.
package weaponskin

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skinwarehouse"
)

const (
	SampleID     = 101609901
	SamplePath   = "equipment/character/swordman/avatar/weapon/101609901.equ"
	ownedStat    = "weapon_skin_demo_beiyun_owned"
	selectedStat = "weapon_skin_demo_selected"
	revisionStat = "weapon_skin_demo_revision"
	ownedPrefix  = "weapon_skin_owned_"
)

type Action uint8

const (
	List Action = iota
	Apply
	Clear
)

var (
	ErrCharacter   = errors.New("武器外观角色不匹配")
	ErrUnavailable = errors.New("武器外观不可用")
	ErrNotOwned    = errors.New("角色没有该武器外观")
	ErrRevision    = errors.New("武器外观状态已变化")
)

type Command struct {
	CharacterID string
	AccountID   string
	Action      Action
	Revision    uint32
	Now         time.Time
	Current     func() bool
	Appearance  uint32
	Available   func(Appearance) bool // 为空时沿用旧单件客户端。
}

type State struct {
	Owned    bool
	Selected uint32
	Revision uint32
	Unlocked string // 字节位图按 Catalog 顺序排列。
}

func BeiyunRegistered(character dnfrepo.CharacterRecord) bool {
	return character.Stats[ownedStat] == SampleID
}

func Selected(character dnfrepo.CharacterRecord) uint32 {
	id := character.Stats[selectedStat]
	if id > 0 && id <= math.MaxUint32 {
		if a, ok := FromAppearance(uint32(id)); ok && a.Allows(character.Job) && Registered(character, a) {
			return a.ID
		}
	}
	return 0
}

func (a Appearance) registrationKey() string {
	if a.ID == SampleID {
		return ownedStat
	}
	return ownedPrefix + strconv.FormatUint(uint64(a.ID), 10)
}

func Registered(character dnfrepo.CharacterRecord, a Appearance) bool {
	return a.ID != 0 && character.Stats[a.registrationKey()] == int64(a.ID)
}

// Update registers the already-owned test avatar on first application. The
// avatar stays in the bag. Only character cosmetic state is saved.
func Update(ctx context.Context, assets dnfrepo.CharacterAssetUnitOfWork, cmd Command) (State, error) {
	var result State
	var rejected error
	if assets == nil || cmd.CharacterID == "" || cmd.AccountID == "" || cmd.Current == nil || cmd.Action > Clear {
		return result, ErrUnavailable
	}
	err := assets.WithinCharacterAssets(ctx, cmd.CharacterID, func(
		characters dnfrepo.CharacterRepository, inventory dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository,
	) error {
		if !cmd.Current() {
			return ErrCharacter
		}
		character, found, err := characters.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != cmd.CharacterID || character.AccountID != cmd.AccountID ||
			(cmd.Available == nil && character.Job != "0") {
			return ErrCharacter
		}
		revision := character.Stats[revisionStat]
		if revision < 0 || revision > math.MaxUint32 {
			return ErrUnavailable
		}
		result = State{Owned: BeiyunRegistered(character), Selected: Selected(character), Revision: uint32(revision)}
		if !result.Owned && character.Job == "0" && cmd.Action != Clear {
			if inventory == nil {
				return ErrUnavailable
			}
			bag, ok, err := inventory.Load(ctx, cmd.CharacterID)
			if err != nil {
				return err
			}
			result.Owned = ok && bag.CharacterID == cmd.CharacterID && ownsSampleAvatar(bag, cmd.Now)
		}
		selected := result.Selected
		if cmd.Available != nil {
			flags := make([]byte, (len(appearances)+7)/8)
			result.Selected = 0
			mark := func(id uint32) {
				a, ok := FromAppearance(id)
				if !ok || !a.Allows(character.Job) || !cmd.Available(a) {
					return
				}
				position := catalogPosition[id]
				flags[position/8] |= 1 << (position % 8)
				if selected == id {
					result.Selected = selected
				}
			}
			if result.Owned {
				mark(SampleID)
			}
			for key, value := range character.Stats {
				if value > 0 && value <= math.MaxUint32 && strings.HasPrefix(key, ownedPrefix) &&
					key == ownedPrefix+strconv.FormatInt(value, 10) {
					mark(uint32(value))
				}
			}
			result.Unlocked = string(flags)
		} else if selected != SampleID {
			result.Selected = 0
			selected = 0
		}
		if cmd.Action == List {
			return nil
		}
		if cmd.Revision != result.Revision {
			rejected = ErrRevision
			return nil
		}
		var target Appearance
		if cmd.Action == Apply {
			id := cmd.Appearance
			if cmd.Available == nil {
				id = SampleID
			}
			a, ok := FromAppearance(id)
			if !ok || !a.Allows(character.Job) || (cmd.Available != nil && !cmd.Available(a)) ||
				(!Registered(character, a) && !(a.ID == SampleID && result.Owned)) {
				rejected = ErrNotOwned
				return nil
			}
			target = a
		}
		if target.ID == selected {
			return nil
		}
		if result.Revision == math.MaxUint32 {
			return ErrUnavailable
		}
		if !cmd.Current() {
			return ErrCharacter
		}
		character = dnfrepo.CloneCharacter(character)
		if character.Stats == nil {
			character.Stats = make(map[string]int64, 3)
		}
		if target.ID != 0 && !Registered(character, target) {
			character.Stats[target.registrationKey()] = int64(target.ID)
			skinwarehouse.RecordAcquisition(character.Stats, skinwarehouse.Weapon, target.ID, cmd.Now)
		}
		character.Stats[selectedStat] = int64(target.ID)
		character.Stats[revisionStat] = revision + 1
		character.UpdatedAt = cmd.Now.UTC()
		if err := dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats); err != nil {
			return err
		}
		result.Selected = target.ID
		result.Revision++
		return nil
	})
	if err != nil {
		return State{}, err
	}
	return result, rejected
}

func ownsSampleAvatar(record dnfrepo.InventoryRecord, now time.Time) bool {
	for slot, stack := range record.Slots {
		if strings.HasPrefix(slot, "1:") && stack.ItemID == SampleID && stack.Count == 1 &&
			(stack.ExpireAt.IsZero() || stack.ExpireAt.After(now)) {
			return true
		}
	}
	return false
}
