package worldmap

// 本文件承载「锦标赛/大会族」在 PVF 侧的结构化解析。
//
// 权威语法来自厂商解析器（本仓不再按 opaque 保留）：
//   - `[required item]` / `[event required item]` / `[added required item]`
//     Tool\PvfLib\Models\DungeonFile.cs:921-958（AppendRequiredItems）：
//     三元组 `ItemId Count Flag`，`Flag == 1` ⇒ ConsumeOnEntry；`ItemId <= 0 || Count <= 0` 的行丢弃；
//     完全相同的 (ItemId, Count, ConsumeOnEntry) 去重。
//   - `[result card]`        DungeonFile.cs:1031-1051：4 元组 `Key GoldWeight ItemWeight EmptyWeight`，
//     整数个数必须是 4 的倍数且非空，否则整个 section 记 malformed。
//   - `[reward item rate]`   DungeonFile.cs:1053-1072：3 元组 `ItemId Weight Count`；
//     厂商允许它出现在 `[result card]` 之内（DungeonFile.cs:700-718）或独立出现（:720-734）。
//   - `[tournament clear reward exp]` DungeonFile.cs:1116-1150：2 元组 `CompletedRounds Experience`，
//     CompletedRounds 必须互不重复且 >= 0。
//   - `[tournament enemies]` / `[tournament start area]` Tool\PvfLib\Models\MapFile.cs:894-988。
//
// 解析失败一律**记 malformed 标志**，不做任何默认值填充（厂商 DungeonFile.cs:688-747 同口径）。

import (
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// TournamentRewardExperience 对应厂商 Tool\PvfLib\Models\DungeonFile.cs:51-55
// 的 `TournamentRewardExperience`（`[tournament clear reward exp]` 的一行）。
type TournamentRewardExperience struct {
	CompletedRounds int64 `json:"completed_rounds"`
	Experience      int64 `json:"experience"`
}

// TournamentResultCard 对应厂商 DungeonFile.cs:57-63 的 `TournamentResultCard`
// （`[result card]` 的一行）。
type TournamentResultCard struct {
	ResultKey   int64 `json:"result_key"`
	GoldWeight  int64 `json:"gold_weight"`
	ItemWeight  int64 `json:"item_weight"`
	EmptyWeight int64 `json:"empty_weight"`
}

// TournamentRewardItemRate 对应厂商 DungeonFile.cs:65-70 的 `TournamentRewardItemRate`
// （`[reward item rate]` 的一行）。
type TournamentRewardItemRate struct {
	ItemID int64 `json:"item_id"`
	Weight int64 `json:"weight"`
	Count  int64 `json:"count"`
}

// TournamentEnemyInfo 对应厂商 MapFile.cs:117-124 的 `TournamentEnemyInfo`。
// PartyCount 是**分桶键**（1..2），第 4 个 token 是 Strength（权重/强度），
// **不是坐标**（MapFile.cs:894-952）。
type TournamentEnemyInfo struct {
	PartyCount int64  `json:"party_count"`
	IsApc      bool   `json:"is_apc,omitempty"`
	Code       int64  `json:"code"`
	Strength   int64  `json:"strength"`
	Name       string `json:"name,omitempty"`
}

// TournamentStartAreaInfo 对应厂商 MapFile.cs:126-132 的 `TournamentStartAreaInfo`。
// 固定 4 元组 `PartyCount X Y Direction`；每张图只有 2 行（双方出生点），
// **不是 15 个对手的坐标**（MapFile.cs:954-988）。
type TournamentStartAreaInfo struct {
	PartyCount int64 `json:"party_count"`
	X          int64 `json:"x"`
	Y          int64 `json:"y"`
	Direction  int64 `json:"direction"`
}

// parseRequiredItemRows 实现厂商 DungeonFile.cs:921-958 的
// `AppendRequiredItems` 语义。返回解析出的三元组；非整数 token ⇒ ok=false。
//
// 注意尾数口径：厂商用 `for (index = 0; index + 2 < values.Length; index += 3)`
// 步进（:934），**不足三个的尾部余数被静默忽略**，不算 malformed。
func parseRequiredItemRows(section RawSection) ([]DungeonRequiredItem, bool) {
	parsed, valid := ints(section)
	if !valid {
		return nil, false
	}
	var out []DungeonRequiredItem
	for index := 0; index+2 < len(parsed); index += 3 {
		itemID := parsed[index]
		count := parsed[index+1]
		if itemID <= 0 || count <= 0 {
			continue
		}
		entry := DungeonRequiredItem{
			ItemID:         itemID,
			Count:          count,
			ConsumeOnEntry: parsed[index+2] == 1,
		}
		duplicate := false
		for _, existing := range out {
			if existing == entry {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		out = append(out, entry)
	}
	return out, true
}

// parseTournamentResultCards 实现厂商 DungeonFile.cs:1031-1051。
func parseTournamentResultCards(section RawSection) ([]TournamentResultCard, bool) {
	parsed, valid := ints(section)
	if !valid || len(parsed) == 0 || len(parsed)%4 != 0 {
		return nil, false
	}
	out := make([]TournamentResultCard, 0, len(parsed)/4)
	for index := 0; index+3 < len(parsed); index += 4 {
		out = append(out, TournamentResultCard{
			ResultKey:   parsed[index],
			GoldWeight:  parsed[index+1],
			ItemWeight:  parsed[index+2],
			EmptyWeight: parsed[index+3],
		})
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// parseTournamentRewardItemRates 实现厂商 DungeonFile.cs:1053-1072。
func parseTournamentRewardItemRates(section RawSection) ([]TournamentRewardItemRate, bool) {
	parsed, valid := ints(section)
	if !valid || len(parsed) == 0 || len(parsed)%3 != 0 {
		return nil, false
	}
	out := make([]TournamentRewardItemRate, 0, len(parsed)/3)
	for index := 0; index+2 < len(parsed); index += 3 {
		out = append(out, TournamentRewardItemRate{
			ItemID: parsed[index],
			Weight: parsed[index+1],
			Count:  parsed[index+2],
		})
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// parseTournamentRewardExperiences 实现厂商 DungeonFile.cs:1116-1150：
// 2 元组、CompletedRounds 必须非负且互不重复。
func parseTournamentRewardExperiences(section RawSection) ([]TournamentRewardExperience, bool) {
	parsed, valid := ints(section)
	if !valid || len(parsed) == 0 || len(parsed)%2 != 0 {
		return nil, false
	}
	seen := make(map[int64]struct{}, len(parsed)/2)
	out := make([]TournamentRewardExperience, 0, len(parsed)/2)
	for index := 0; index+1 < len(parsed); index += 2 {
		completedRounds := parsed[index]
		experience := parsed[index+1]
		if completedRounds < 0 || experience < 0 {
			return nil, false
		}
		if _, duplicate := seen[completedRounds]; duplicate {
			return nil, false
		}
		seen[completedRounds] = struct{}{}
		out = append(out, TournamentRewardExperience{
			CompletedRounds: completedRounds,
			Experience:      experience,
		})
	}
	return out, true
}

// parseTournamentEnemies 实现厂商 MapFile.cs:894-952：
// `PartyCount(>0) + [monster]|[apc] + (Code Strength Name)*3`。
// tokens.Count >= 5、(count-2) % 3 == 0 是硬要求。
func parseTournamentEnemies(section RawSection) ([]TournamentEnemyInfo, bool) {
	tokens := values(section)
	if len(tokens) < 5 {
		return nil, false
	}
	if tokens[0].Kind != dnfpvf.TokenInt || tokens[0].Int <= 0 {
		return nil, false
	}
	partyCount := tokens[0].Int
	if !isText(tokens[1]) {
		return nil, false
	}
	kind := sectionKey(tokens[1].Value)
	isApc := kind == "[apc]"
	if !isApc && kind != "[monster]" {
		return nil, false
	}
	if (len(tokens)-2)%3 != 0 {
		return nil, false
	}
	out := make([]TournamentEnemyInfo, 0, (len(tokens)-2)/3)
	for index := 2; index+2 < len(tokens); index += 3 {
		if tokens[index].Kind != dnfpvf.TokenInt || tokens[index+1].Kind != dnfpvf.TokenInt {
			return nil, false
		}
		if !isText(tokens[index+2]) {
			return nil, false
		}
		out = append(out, TournamentEnemyInfo{
			PartyCount: partyCount,
			IsApc:      isApc,
			Code:       tokens[index].Int,
			Strength:   tokens[index+1].Int,
			Name:       tokens[index+2].Value,
		})
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// parseTournamentStartAreas 实现厂商 MapFile.cs:954-988：
// 固定 4 元组 `PartyCount X Y Direction`，token 数必须是 4 的倍数，PartyCount > 0。
func parseTournamentStartAreas(section RawSection) ([]TournamentStartAreaInfo, bool) {
	parsed, valid := ints(section)
	if !valid || len(parsed) == 0 || len(parsed)%4 != 0 {
		return nil, false
	}
	out := make([]TournamentStartAreaInfo, 0, len(parsed)/4)
	for index := 0; index+3 < len(parsed); index += 4 {
		if parsed[index] <= 0 {
			return nil, false
		}
		out = append(out, TournamentStartAreaInfo{
			PartyCount: parsed[index],
			X:          parsed[index+1],
			Y:          parsed[index+2],
			Direction:  parsed[index+3],
		})
	}
	return out, true
}

// tournamentEnemySectionKey / tournamentStartAreaSectionKey 是这两个 section 的
// canonical key（parse_map.go 的分派与 worldmap 其余部分共用）。
const (
	tournamentEnemiesSectionKey   = "tournament enemies"
	tournamentStartAreaSectionKey = "tournament start area"
)

// requiredItemSectionKeys 是厂商 DungeonFile.cs:642-657 注册的三个 `[required item]` 家族段名。
var requiredItemSectionKeys = []string{"required item", "event required item", "added required item"}

// tournamentRewardSectionKeys 是厂商 `[result card]` / `[reward item rate]` /
// `[tournament clear reward exp]` 三个段名。
const (
	resultCardSectionKey               = "result card"
	rewardItemRateSectionKey           = "reward item rate"
	tournamentClearRewardExpSectionKey = "tournament clear reward exp"
)

// directBlockSection 返回一个 block 段的**直接数据 token**。
//
// 为什么需要它：本仓的 `parse`/`rawSections` 是"扁平 section"模型，block 的子节点
// 各自成段，因此
//
//	[result card]
//	[reward item rate]
//	3329 60 1 …
//	[/reward item rate]
//	0 0 0 100 1 70 0 30 …        ← 这一行挂在 `[/reward item rate]` 上
//	[/result card]
//
// 里 `[result card]` 自己拿到 **0 个 token**，真正的 24 个整数归它的直接子关闭段。
// 厂商 `DungeonFile.cs:1031-1051` 读的是 `node.DataItems`（该节点的直接数据行），
// 语义上就是"本段 token + 直接子关闭段的 token"。本函数按同一口径合并：
// 收集所有 scope 最后一跳等于本段名的**关闭**段（即本段的直接子关闭段），
// **不**收集孙子层（它们的 scope 更深）也不收集开启子段（它们的数据属于自己）。
func directBlockSection(sections []RawSection, section RawSection) RawSection {
	if !section.Block {
		return section
	}
	key := sectionKey(section.Name)
	if key == "" || isClosingSection(key) {
		return section
	}
	merged := cloneRawSection(section)
	for _, candidate := range sections {
		candidateKey := sectionKey(candidate.Name)
		if !isClosingSection(candidateKey) {
			continue
		}
		if len(candidate.Scope) == 0 {
			continue
		}
		if sectionKey(candidate.Scope[len(candidate.Scope)-1]) != key {
			continue
		}
		if len(candidate.Tokens) == 0 {
			continue
		}
		merged.Tokens = append(merged.Tokens, candidate.Tokens...)
	}
	return merged
}
