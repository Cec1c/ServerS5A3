package dnfbridge

import (
	"fmt"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// ── 锦标赛定义投影 ───────────────────────────────────────────────────────────
//
// 逐条对拍厂商 GameWorld\TournamentDungeonDefinition.cs:250-510 的
// `TournamentDungeonDefinitionCatalog.TryProject`。厂商对每一项都是**硬失败**：
// `out failureReason` + `return false`，绝不填默认值、绝不"少几个对手也能打"。
// 本文件保持同一口径。

// tournamentActorKind 对应厂商 `TournamentActorKind`。
type tournamentActorKind int

const (
	tournamentActorMonster tournamentActorKind = iota
	tournamentActorAICharacter
)

// tournamentActorDefinition 对应厂商 `TournamentActorDefinition`
// （TournamentDungeonDefinition.cs:398-438 构造）。
type tournamentActorDefinition struct {
	PartyCount int64
	Kind       tournamentActorKind
	IsApc      bool
	Code       int64
	Strength   int64
	Name       string
	Level      byte
	ActorType  byte
}

// tournamentResultCardDefinition 对应厂商 `TournamentResultCardDefinition`。
type tournamentResultCardDefinition struct {
	ResultKey   int64
	GoldWeight  int64
	ItemWeight  int64
	EmptyWeight int64
}

// TotalWeight 对应厂商 `TournamentResultCardDefinition.TotalWeight`。
func (c tournamentResultCardDefinition) TotalWeight() int64 {
	return c.GoldWeight + c.ItemWeight + c.EmptyWeight
}

// tournamentDefinition 对应厂商 `TournamentDungeonDefinition`
// （TournamentDungeonDefinition.cs:494-508 的构造实参）。
type tournamentDefinition struct {
	DungeonID           int64
	MapID               int64
	BasisLevel          byte
	PartyLimit          int
	CoinLimit           int64
	RoundFatigue        int64
	ClearRewardGoldRate int64
	// RewardExperience 的键是 CompletedRounds（0..4），厂商要求 5 个键齐全。
	RewardExperience map[int]int64
	// ResultCards 的键是 ResultKey（0..5），厂商要求 6 个键齐全且 TotalWeight > 0。
	ResultCards map[int]tournamentResultCardDefinition
	// RewardItemRates 必须非空且权重合计 > 0。
	RewardItemRates []worldmap.TournamentRewardItemRate
	Candidates      []tournamentActorDefinition
	StartAreas      []worldmap.TournamentStartAreaInfo
	// EntryItems 是 `[required item]` 家族（厂商 :479-492 只吃 dungeon.RequiredItems）。
	EntryItems []dnfdungeon.EntryItemRequirement
}

// tournamentApcLevelResolver 是厂商
// `DungeonActorTemplateProjector.TryGetAiCharacterLevel(code, out level)`
// （TournamentDungeonDefinition.cs:418-425）的等价物。返回 false 时整个副本
// 准备**硬失败**，不做 fallback。
type tournamentApcLevelResolver func(code int64) (byte, bool)

// tournamentActorsUseApc 报告该投影是否走厂商的 `[apc]` 分支
// （TournamentDungeonDefinition.cs:416-428）。本批只实现 `[monster]`，但解析层
// 与定义层都保留 `[apc]` 分支的判定与硬失败，避免"静默把它当怪打"。
func (d tournamentDefinition) tournamentActorsUseApc() bool {
	for _, candidate := range d.Candidates {
		if candidate.IsApc {
			return true
		}
	}
	return false
}

// resolveTournamentDungeonDefinition 是厂商 `TryProject` 的移植。
// 返回 (definition, failureReason, ok)。失败时 ok=false 且 failureReason 非空，
// **不允许调用方忽略失败原因继续铺怪**。
func resolveTournamentDungeonDefinition(
	dungeon worldmap.Dungeon,
	mapValue worldmap.Map,
	apcLevel tournamentApcLevelResolver,
) (tournamentDefinition, string, bool) {
	var definition tournamentDefinition

	// 厂商 :259-263 `if (!dungeon.TournamentDungeon)`。
	if !dungeon.Metadata.Flags["tournament dungeon"] {
		return definition, "dungeon is not marked as tournament", false
	}
	basisLevel := dungeon.Metadata.BasisLevel.Value
	// 厂商 :264-268。
	if !dungeon.Metadata.BasisLevel.Set || basisLevel <= 0 || basisLevel > 255 {
		return definition, "tournament basic level is invalid", false
	}
	partyLimit := dungeon.Metadata.LimitPartyCount.Value
	// 厂商 :269-273。
	if !dungeon.Metadata.LimitPartyCount.Set || partyLimit < 1 || partyLimit > 2 {
		return definition, "tournament party limit is unsupported", false
	}
	coinLimit, coinLimitSet := dungeon.Metadata.IntegerValues["coin limit"]
	roundFatigue, roundFatigueSet := dungeon.Metadata.IntegerValues["tournament round fatigue"]
	goldRate, goldRateSet := dungeon.Metadata.IntegerValues["tournament clear reward gold rate"]
	// 厂商 :274-282。厂商里 CoinLimit/TournamentRoundFatigue 缺段按 0 处理（C# 默认值），
	// 但 GoldRate <= 0 一定失败 —— 本仓同样把"缺段"归 0 看待，只有 gold rate 必须显式 > 0。
	if !coinLimitSet {
		coinLimit = 0
	}
	if !roundFatigueSet {
		roundFatigue = 0
	}
	if coinLimit < 0 || roundFatigue < 0 || !goldRateSet || goldRate <= 0 {
		return definition, "tournament DGN reward or round fields are incomplete", false
	}

	// 厂商 :283-289。
	if dungeon.Metadata.TournamentRewardExpMalformed ||
		dungeon.Metadata.TournamentResultCardMalformed ||
		dungeon.Metadata.TournamentRewardItemRateMalformed {
		return definition, "tournament reward definition is malformed", false
	}

	// 厂商 :291-316：CompletedRounds 必须落在 0..4、互不重复、0..4 五个键齐全。
	rewardExperience := make(map[int]int64, len(dungeon.Metadata.TournamentClearExperiences))
	for _, reward := range dungeon.Metadata.TournamentClearExperiences {
		if reward.CompletedRounds < 0 || reward.CompletedRounds > tournamentRoundCount ||
			reward.Experience < 0 || reward.Experience > 0xFFFFFFFF {
			return definition, "tournament reward experience contains an invalid row", false
		}
		if _, duplicate := rewardExperience[int(reward.CompletedRounds)]; duplicate {
			return definition, "tournament reward experience contains an invalid row", false
		}
		rewardExperience[int(reward.CompletedRounds)] = reward.Experience
	}
	for round := 0; round <= tournamentRoundCount; round++ {
		if _, ok := rewardExperience[round]; !ok {
			return definition, fmt.Sprintf("tournament reward experience misses round %d", round), false
		}
	}

	// 厂商 :318-354：ResultKey 必须落在 0..5、TotalWeight 必须 > 0、0..5 六个键齐全。
	resultCards := make(map[int]tournamentResultCardDefinition, len(dungeon.Metadata.TournamentResultCards))
	for _, card := range dungeon.Metadata.TournamentResultCards {
		if card.ResultKey < 0 || card.ResultKey > 5 ||
			card.GoldWeight < 0 || card.ItemWeight < 0 || card.EmptyWeight < 0 {
			return definition, "tournament result card contains an invalid row", false
		}
		projected := tournamentResultCardDefinition{
			ResultKey:   card.ResultKey,
			GoldWeight:  card.GoldWeight,
			ItemWeight:  card.ItemWeight,
			EmptyWeight: card.EmptyWeight,
		}
		if projected.TotalWeight() <= 0 {
			return definition, "tournament result card weights are invalid", false
		}
		if _, duplicate := resultCards[int(card.ResultKey)]; duplicate {
			return definition, "tournament result card weights are invalid", false
		}
		resultCards[int(card.ResultKey)] = projected
	}
	for resultKey := 0; resultKey <= 5; resultKey++ {
		if _, ok := resultCards[resultKey]; !ok {
			return definition, fmt.Sprintf("tournament result card misses key %d", resultKey), false
		}
	}

	// 厂商 :356-384。
	var rewardItemWeightTotal int64
	rewardItemRates := make([]worldmap.TournamentRewardItemRate, 0, len(dungeon.Metadata.TournamentRewardItemRates))
	for _, rate := range dungeon.Metadata.TournamentRewardItemRates {
		if rate.ItemID <= 0 || rate.Weight <= 0 || rate.Count <= 0 {
			return definition, "tournament reward item rate contains an invalid row", false
		}
		rewardItemWeightTotal += rate.Weight
		if rewardItemWeightTotal > 0x7FFFFFFF {
			return definition, "tournament reward item rate total exceeds protocol range", false
		}
		rewardItemRates = append(rewardItemRates, rate)
	}
	if len(rewardItemRates) == 0 || rewardItemWeightTotal <= 0 {
		return definition, "tournament reward item rate is missing", false
	}

	// 厂商 :386-396：MAP 必须属于同一个 dungeon，且不能是畸形定义。
	if mapValue.DungeonID.Set && mapValue.DungeonID.Value != dungeon.ID {
		return definition, "tournament MAP belongs to another dungeon", false
	}
	if mapValue.TournamentDefinitionMalformed {
		return definition, "tournament MAP definition is malformed", false
	}

	// 厂商 :398-438：候选池校验 + `[apc]` 的 AICharacter 等级硬要求。
	candidates := make([]tournamentActorDefinition, 0, len(mapValue.TournamentEnemyCandidates))
	for _, candidate := range mapValue.TournamentEnemyCandidates {
		if candidate.PartyCount < 1 || candidate.PartyCount > 2 ||
			candidate.Code <= 0 || candidate.Strength < 0 ||
			len([]rune(candidate.Name)) == 0 {
			return definition, "tournament candidate contains an invalid field", false
		}
		level := byte(basisLevel)
		actorType := byte(0)
		kind := tournamentActorMonster
		if candidate.IsApc {
			if apcLevel == nil {
				return definition, fmt.Sprintf("tournament APC definition is missing code=%d", candidate.Code), false
			}
			resolved, ok := apcLevel(candidate.Code)
			if !ok {
				return definition, fmt.Sprintf("tournament APC definition is missing code=%d", candidate.Code), false
			}
			level = resolved
			kind = tournamentActorAICharacter
			actorType = 5
		}
		candidates = append(candidates, tournamentActorDefinition{
			PartyCount: candidate.PartyCount,
			Kind:       kind,
			IsApc:      candidate.IsApc,
			Code:       candidate.Code,
			Strength:   candidate.Strength,
			Name:       candidate.Name,
			Level:      level,
			ActorType:  actorType,
		})
	}

	// 厂商 :440-463：出生点字段范围校验。
	startAreas := make([]worldmap.TournamentStartAreaInfo, 0, len(mapValue.TournamentStartAreas))
	for _, area := range mapValue.TournamentStartAreas {
		if area.PartyCount < 1 || area.PartyCount > 2 ||
			area.X < 0 || area.X > 0xFFFF ||
			area.Y < 0 || area.Y > 0xFFFF ||
			area.Direction < 0 || area.Direction > 0xFF {
			return definition, "tournament start area contains an invalid field", false
		}
		startAreas = append(startAreas, area)
	}

	// 厂商 :465-477：候选池 15×人数、出生点 ≥2 是硬门槛。
	if countTournamentCandidates(candidates, partyLimit) < tournamentNpcTeamCount*int(partyLimit) {
		return definition, fmt.Sprintf("tournament candidate pool is too small for party count %d", partyLimit), false
	}
	if countTournamentStartAreas(startAreas, partyLimit) < tournamentRequiredStartAreas {
		return definition, fmt.Sprintf("tournament start areas are incomplete for party count %d", partyLimit), false
	}

	// 厂商 :479-492。
	entryItems := make([]dnfdungeon.EntryItemRequirement, 0, len(dungeon.Metadata.RequiredItems))
	for _, item := range dungeon.Metadata.RequiredItems {
		if item.ItemID <= 0 || item.Count <= 0 {
			return definition, "tournament entry item contains an invalid field", false
		}
		entryItems = append(entryItems, dnfdungeon.EntryItemRequirement{
			ItemID:         item.ItemID,
			Count:          item.Count,
			ConsumeOnEntry: item.ConsumeOnEntry,
		})
	}

	definition = tournamentDefinition{
		DungeonID:           dungeon.ID,
		MapID:               mapValue.ID,
		BasisLevel:          byte(basisLevel),
		PartyLimit:          int(partyLimit),
		CoinLimit:           coinLimit,
		RoundFatigue:        roundFatigue,
		ClearRewardGoldRate: goldRate,
		RewardExperience:    rewardExperience,
		ResultCards:         resultCards,
		RewardItemRates:     rewardItemRates,
		Candidates:          candidates,
		StartAreas:          startAreas,
		EntryItems:          entryItems,
	}
	return definition, "", true
}

// countTournamentCandidates 对应厂商 TournamentDungeonDefinition.cs:512-523。
func countTournamentCandidates(candidates []tournamentActorDefinition, partyCount int64) int {
	count := 0
	for _, candidate := range candidates {
		if candidate.PartyCount == partyCount {
			count++
		}
	}
	return count
}

// countTournamentStartAreas 对应厂商 TournamentDungeonDefinition.cs:525-536。
func countTournamentStartAreas(areas []worldmap.TournamentStartAreaInfo, partyCount int64) int {
	count := 0
	for _, area := range areas {
		if area.PartyCount == partyCount {
			count++
		}
	}
	return count
}
