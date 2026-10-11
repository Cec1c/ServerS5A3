package dungeon

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

var (
	ErrEntryLevelTooLow      = errors.New("dnf dungeon character level is too low")
	ErrEntryFatigueUnknown   = errors.New("dnf dungeon character fatigue is unavailable")
	ErrEntryFatigueExhausted = errors.New("dnf dungeon character fatigue is exhausted")
	ErrEntryPartyLimit       = errors.New("dnf dungeon party exceeds PVF limit")
	// ErrEntryRequiredItemMissing 对应厂商 `[required item]` 准入失败
	// （Network\Handlers\Dungeon\DungeonEntryAdmissionApplicationService.cs:170-183
	// 用 tournamentDefinition.EntryItems 收取），以及
	// GameWorld\TournamentDungeonDefinition.cs:479-492 的 entry item 硬校验。
	ErrEntryRequiredItemMissing = errors.New("dnf dungeon entry item is missing")
	// ErrEntryRequiredItemUnknown 表示调用方没有提供持仓事实，而 PVF 声明了
	// `[required item]`。厂商口径是"缺项报错"，不是"当 0 处理"。
	ErrEntryRequiredItemUnknown = errors.New("dnf dungeon entry item ownership is unknown")
)

// EntryItemRequirement 是 `[required item]` 的一条结构化要求
// （厂商 Tool\PvfLib\Models\DungeonFile.cs:38-43 的 DungeonRequiredItem）。
type EntryItemRequirement struct {
	ItemID         int64
	Count          int64
	ConsumeOnEntry bool
}

// EntryPolicy contains only the proved PVF and persisted-character facts used
// to decide whether a character may enter a dungeon. Transport/session code is
// responsible for taking a stable party snapshot before calling ValidateEntry.
type EntryPolicy struct {
	CharacterLevel      int
	MinimumLevel        int64
	MinimumLevelSet     bool
	PartyCount          int
	PartyLimit          int64
	PartyLimitSet       bool
	NoFatigue           bool
	EnterWithoutFatigue bool
	Fatigue             int64
	FatigueKnown        bool

	// RequiredItems 是 `[required item]` 的结构化内容。为空表示该 DGN 没有声明
	// 准入道具（绝大多数副本），既有行为完全不变。
	RequiredItems []EntryItemRequirement
	// ItemCounts 是调用方从持久化背包读到的"道具 id -> 总持有数"。nil 表示
	// 未读取；此时只要 RequiredItems 非空就按 ErrEntryRequiredItemUnknown 拒绝。
	ItemCounts map[int64]int64
}

func ValidateEntry(policy EntryPolicy) error {
	if policy.MinimumLevelSet && int64(policy.CharacterLevel) < policy.MinimumLevel {
		return fmt.Errorf(
			"%w: level=%d required=%d",
			ErrEntryLevelTooLow,
			policy.CharacterLevel,
			policy.MinimumLevel,
		)
	}
	partyCount := policy.PartyCount
	if partyCount < 1 {
		partyCount = 1
	}
	if policy.PartyLimitSet && policy.PartyLimit > 0 && int64(partyCount) > policy.PartyLimit {
		return fmt.Errorf("%w: members=%d limit=%d", ErrEntryPartyLimit, partyCount, policy.PartyLimit)
	}
	if err := validateEntryRequiredItems(policy); err != nil {
		return err
	}
	if policy.NoFatigue || policy.EnterWithoutFatigue {
		return nil
	}
	if !policy.FatigueKnown {
		return ErrEntryFatigueUnknown
	}
	if policy.Fatigue <= 0 {
		return fmt.Errorf("%w: fatigue=%d", ErrEntryFatigueExhausted, policy.Fatigue)
	}
	return nil
}

// validateEntryRequiredItems 按厂商 DungeonFile.cs:934-955 解析出的三元组逐条校验
// 角色是否持有足够数量。厂商对 `[required item]` 是**准入硬条件**；本仓此前把它当
// opaque 段丢弃，等于"证物形同虚设"（见核查报告 Q2.4）。
func validateEntryRequiredItems(policy EntryPolicy) error {
	if len(policy.RequiredItems) == 0 {
		return nil
	}
	if policy.ItemCounts == nil {
		return fmt.Errorf("%w: requirements=%d", ErrEntryRequiredItemUnknown, len(policy.RequiredItems))
	}
	for _, requirement := range policy.RequiredItems {
		if requirement.ItemID <= 0 || requirement.Count <= 0 {
			continue
		}
		if policy.ItemCounts[requirement.ItemID] < requirement.Count {
			return fmt.Errorf(
				"%w: item=%d held=%d required=%d",
				ErrEntryRequiredItemMissing,
				requirement.ItemID,
				policy.ItemCounts[requirement.ItemID],
				requirement.Count,
			)
		}
	}
	return nil
}

func CharacterStat(stats map[string]int64, names ...string) (int64, bool) {
	for _, name := range names {
		if value, ok := stats[name]; ok {
			return value, true
		}
	}
	return 0, false
}

// SameSelectRequest defines idempotent current-client op16 replay identity.
// Opaque tails are deliberately excluded until current-EXE evidence proves
// their semantics.
func SameSelectRequest(left, right dungeoncmd.SelectDungeonRequest) bool {
	return left.DungeonID == right.DungeonID &&
		left.Difficulty == right.Difficulty &&
		left.EntryOption == right.EntryOption &&
		left.SelectionMode == right.SelectionMode &&
		left.RuntimeState == right.RuntimeState &&
		left.RuntimeToken == right.RuntimeToken &&
		left.Reserved == right.Reserved &&
		left.PartyState == right.PartyState &&
		left.LeaderObjectKey == right.LeaderObjectKey &&
		left.SpecialMode == right.SpecialMode &&
		len(left.OpaqueTail) == 0 && len(right.OpaqueTail) == 0
}

func RuntimeOwnsCharacter(storedCharacterID string, selectedCharacterID uint16) bool {
	return selectedCharacterID != 0 &&
		strings.TrimSpace(storedCharacterID) == strconv.Itoa(int(selectedCharacterID))
}
