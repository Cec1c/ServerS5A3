package progression

import (
	"fmt"
	"math"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

type questExperienceRules struct {
	baseByLevel      []uint32
	goldByLevel      []uint32
	difficultyWeight map[string]int
	greenPenalty     int
	greyPenalty      int
	// epicGreenPenalty / epicGreyPenalty 是 `[epic green level penalty]` /
	// `[epic grey level penalty]` 的百分比档。参考实现（ServerS4A21
	// `GameWorld/QuestRewardProjector.cs:220-231` 的 `ComputeLevelPenalty`）对
	// `[epic]` 主线用另一组罚则；本仓的运行时 PVF 里这两档都是 100（= 不罚）。
	epicGreenPenalty int
	epicGreyPenalty  int
}

// questLevelPenalty 是参考实现 `QuestParameterTable.ComputeLevelPenalty` 的逐字端口：
// 等级差 7..11 走 green 档、>11 走 grey 档，`[epic]` 用 epic 档，其余 100（不罚）。
// 返回的是**百分比**：100 = 不罚，80 = 只给 80%。
func (r questExperienceRules) questLevelPenalty(levelDiff int, questGrade string) int {
	isEpic := normalizeQuestGrade(questGrade) == "epic"
	switch {
	case levelDiff > 11:
		if isEpic {
			return r.epicGreyPenalty
		}
		return r.greyPenalty
	case levelDiff > 6:
		if isEpic {
			return r.epicGreenPenalty
		}
		return r.greenPenalty
	default:
		return 100
	}
}

func normalizeQuestGrade(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Trim(value, "`")
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "[]")
	return strings.TrimSpace(value)
}

// DungeonClearBaseExperience returns the exact level row from the runtime
// PVF [exp reward table]. It deliberately applies no difficulty, rank,
// premium, party, or account modifier; the dungeon settlement owner must
// combine only modifiers it has independently proved.
func (t *Tables) DungeonClearBaseExperience(dungeonBasisLevel int) (uint32, error) {
	if t == nil || dungeonBasisLevel <= 0 || dungeonBasisLevel > len(t.quest.baseByLevel) {
		return 0, fmt.Errorf(
			"%w: dungeon basis level=%d entries=%d",
			ErrQuestExperienceLookup,
			dungeonBasisLevel,
			func() int {
				if t == nil {
					return 0
				}
				return len(t.quest.baseByLevel)
			}(),
		)
	}
	return t.quest.baseByLevel[dungeonBasisLevel-1], nil
}

// QuestExperience computes the PVF quest-parameter value only. It is a dormant
// domain rule until a quest definition's reward fields and the current EXE's
// settlement category/timing are both proven.
//
// 公式逐字对齐参考实现 `QuestParameterTable.ComputeExp`
// （ServerS4A21 `GameWorld/QuestRewardProjector.cs:175-199`）：
//
//	weighted = base * (1000 + weight) / 1000      // `[difficulty]` 是**千分 additive**，不是百分比
//	value    = weighted * penalty / 100           // penalty 是百分比（100 = 不罚）
//
// 三处此前的偏差（现场报障「主线给的经验少 lv10-lv11」）：
//   - 旧实现把 weight 当百分比：`penalty * ((weight*base/100)/100)`。对常见难度
//     `G`(30)/`S`(10) 等于只给 0.3/0.1 倍 base（比对手册少 3.4~10 倍），
//     对 `1`(1780) 又反而多给 6.4 倍。
//   - 罚则档位读的是 section 的**第一个**整数（`7`/`12`，那是等级差下限），
//     正确值是最后一个整数（`80`/`30`）。见 parseQuestExperienceRules。
//   - `[difficulty]` 档位是**字符串键**：PVF 同时声明单字符档（字母、`1`..`9`）
//     与两位数档（`10`..`17`）。旧实现只接受单 rune 键，parse 丢掉 `10`..`17`，
//     且任务难度 `[difficulty] 10`/`17` 在 catalog 里被静默退回默认 `G`(30)，
//     令使用这些键的任务少给约 40% 经验。真实归档含 378 份相关资源，
//     当前目录含 243 项定义（安徒恩/镜像贝希摩斯/卢克等），不等于当前可接数。
//     见 parseQuestExperienceRules 与 quest.Catalog.PlanFinishReward。
func (t *Tables) QuestExperience(playerLevel, questMinLevel int, difficulty string, questGrade string, ignoreLevel bool) (uint32, error) {
	if t == nil || playerLevel <= 0 || questMinLevel <= 0 {
		return 0, fmt.Errorf("%w: player=%d quest_min=%d", ErrLevelOutOfRange, playerLevel, questMinLevel)
	}
	lookupLevel := questMinLevel
	if ignoreLevel {
		lookupLevel = playerLevel
	}
	if lookupLevel > len(t.quest.baseByLevel) {
		return 0, fmt.Errorf("%w: level=%d entries=%d", ErrQuestExperienceLookup, lookupLevel, len(t.quest.baseByLevel))
	}
	weight, ok := t.quest.difficultyWeight[normalizeDifficulty(difficulty)]
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrQuestDifficulty, difficulty)
	}
	penalty := 100
	if !ignoreLevel {
		penalty = t.quest.questLevelPenalty(playerLevel-questMinLevel, questGrade)
	}
	base := uint64(t.quest.baseByLevel[lookupLevel-1])
	weighted := base * uint64(1000+weight) / 1000
	value := weighted * uint64(penalty) / 100
	if value > math.MaxUint32 {
		return 0, fmt.Errorf("%w: quest experience=%d", ErrExperienceOutOfRange, value)
	}
	return uint32(value), nil
}

// QuestGold ports the C# QuestParameterTable.ComputeGoldReward domain rule:
// [gold reward table] supplies the base row and quest [gold multiple] (or a
// reward-int item 0 row with count) supplies the percentage multiplier. This is
// pure PVF/accounting data; packet ACK shape is still owned by the bridge.
func (t *Tables) QuestGold(playerLevel, questMinLevel, goldMultiple int, ignoreLevel bool) (uint32, error) {
	if t == nil || playerLevel <= 0 || questMinLevel <= 0 {
		return 0, fmt.Errorf("%w: player=%d quest_min=%d", ErrLevelOutOfRange, playerLevel, questMinLevel)
	}
	if len(t.quest.goldByLevel) == 0 {
		return 0, fmt.Errorf("%w: %s [gold reward table]", ErrTableEmpty, QuestParameterPath)
	}
	if goldMultiple <= 0 {
		goldMultiple = 100
	}
	lookupLevel := questMinLevel
	if ignoreLevel {
		lookupLevel = playerLevel
	}
	if lookupLevel > len(t.quest.goldByLevel) {
		return 0, fmt.Errorf("%w: level=%d entries=%d", ErrQuestExperienceLookup, lookupLevel, len(t.quest.goldByLevel))
	}
	penalty := 100
	if !ignoreLevel {
		// 参考实现的 `ComputeGoldReward` 传的是空 questGrade，即金币永远走
		// 普通（非 epic）罚则；这里保持同一口径。
		penalty = t.quest.questLevelPenalty(playerLevel-questMinLevel, "")
	}
	value := uint64(goldMultiple) * uint64(penalty) * uint64(t.quest.goldByLevel[lookupLevel-1]) / 100 / 100
	if value > math.MaxUint32 {
		return 0, fmt.Errorf("%w: quest gold=%d", ErrExperienceOutOfRange, value)
	}
	return uint32(value), nil
}

func parseQuestExperienceRules(doc *dnfpvf.Document) (questExperienceRules, error) {
	if doc == nil {
		return questExperienceRules{}, fmt.Errorf("%w: %s", ErrTableEmpty, QuestParameterPath)
	}
	rules := questExperienceRules{
		difficultyWeight: make(map[string]int),
	}
	expValues := doc.Ints("exp reward table")
	if len(expValues) == 0 || len(expValues)%2 != 0 {
		return questExperienceRules{}, fmt.Errorf("%w: %s [exp reward table] integers=%d", ErrTableMalformed, QuestParameterPath, len(expValues))
	}
	rules.baseByLevel = make([]uint32, 0, len(expValues)/2)
	for index := 0; index < len(expValues); index += 2 {
		value := expValues[index]
		if value < 0 || value > math.MaxUint32 {
			return questExperienceRules{}, fmt.Errorf("%w: %s quest base=%d", ErrTableMalformed, QuestParameterPath, value)
		}
		rules.baseByLevel = append(rules.baseByLevel, uint32(value))
	}
	goldValues := doc.Ints("gold reward table")
	// Gold is one value per level; only the EXP table has paired sentinels.
	rules.goldByLevel = make([]uint32, 0, len(goldValues))
	for _, value := range goldValues {
		if value < 0 || value > math.MaxUint32 {
			return questExperienceRules{}, fmt.Errorf("%w: %s quest gold=%d", ErrTableMalformed, QuestParameterPath, value)
		}
		rules.goldByLevel = append(rules.goldByLevel, uint32(value))
	}
	difficultyTokens := sectionTokens(doc, "difficulty")
	for index := 0; index < len(difficultyTokens); index++ {
		token := difficultyTokens[index]
		if token.Kind != dnfpvf.TokenString && token.Kind != dnfpvf.TokenIdent {
			continue
		}
		key := normalizeDifficulty(token.Value)
		if key == "" {
			continue
		}
		for next := index + 1; next < len(difficultyTokens); next++ {
			if difficultyTokens[next].Kind != dnfpvf.TokenInt {
				continue
			}
			value := difficultyTokens[next].Int
			if value <= 0 || value > math.MaxInt {
				return questExperienceRules{}, fmt.Errorf("%w: %s difficulty=%q weight=%d", ErrTableMalformed, QuestParameterPath, key, value)
			}
			rules.difficultyWeight[key] = int(value)
			index = next
			break
		}
	}
	if len(rules.difficultyWeight) == 0 {
		return questExperienceRules{}, fmt.Errorf("%w: %s [difficulty]", ErrTableEmpty, QuestParameterPath)
	}
	// 罚则 section 的真实形状是 `minDiff maxDiff percent`
	//（运行时 PVF `n_quest/questParameter.etc`：
	//
	//	[green level penalty]      7 11 80
	//	[grey level penalty]      12 90 30
	//	[epic green level penalty]  7 11 100
	//	[epic grey level penalty]  12 200 100
	//
	// ），所以**最后一个**整数才是罚则百分比；第一个整数是等级差下限。
	// 参考实现 `QuestParameterTable.Parse` 用 `line.Split(' ')[0]` 读的是第一个整数，
	// 与它自己的默认值（green 80 / grey 30，见 `QuestRewardProjector.cs:170-173`）
	// 和它的 self-test（`A21TutorialProtocolSelfTest.cs:1697-1708` 用 `[green level penalty]\n80`
	// 单值形状）都自相矛盾 —— 那份实现只是碰巧没遇到多值 section。这里按 section 形状
	// 取最后一个整数，同时兼容单值形状（`80`）。
	green, _, err := questPenaltyPercent(doc, "green level penalty")
	if err != nil {
		return questExperienceRules{}, err
	}
	grey, _, err := questPenaltyPercent(doc, "grey level penalty")
	if err != nil {
		return questExperienceRules{}, err
	}
	// 两个 epic 档在运行时 PVF 里都存在（都是 100 = 不罚）；PVF 没写时不发明罚则，
	// 直接沿用普通档（= 没有 epic 特例）。
	epicGreen, hasEpicGreen, err := questPenaltyPercent(doc, "epic green level penalty")
	if err != nil {
		return questExperienceRules{}, err
	}
	if !hasEpicGreen {
		epicGreen = green
	}
	epicGrey, hasEpicGrey, err := questPenaltyPercent(doc, "epic grey level penalty")
	if err != nil {
		return questExperienceRules{}, err
	}
	if !hasEpicGrey {
		epicGrey = grey
	}
	rules.greenPenalty = green
	rules.greyPenalty = grey
	rules.epicGreenPenalty = epicGreen
	rules.epicGreyPenalty = epicGrey
	return rules, nil
}

// questPenaltyPercent 从 `[<name> level penalty]` section 取罚则百分比。
//
// 该 section 在运行时 PVF 里是 `minDiff maxDiff percent` 三个整数，百分数是最后一个；
// 单值形状（参考实现 self-test 的 `[green level penalty]\n80`）也接受。
// 缺失时返回 present=false 交给调用方决定回退，而不是把 0 当成"罚到 0 经验"。
func questPenaltyPercent(doc *dnfpvf.Document, name string) (value int, present bool, err error) {
	values := doc.Ints(name)
	if len(values) == 0 {
		return 0, false, nil
	}
	last := values[len(values)-1]
	if last < 0 || last > 1000 {
		return 0, false, fmt.Errorf("%w: %s [%s]=%d", ErrTableMalformed, QuestParameterPath, name, last)
	}
	return int(last), true, nil
}

// normalizeDifficulty keys the `[difficulty]` weight table. The runtime PVF
// declares one-character ranks (letters, `1`..`9`) and two-digit ranks
// (`10`..`17`), so the whole trimmed token — upper-cased, since the data mixes
// `f`/`F` — is the key rather than a single rune.
func normalizeDifficulty(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}
