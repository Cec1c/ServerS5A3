package dnfbridge

import (
	"errors"
	"fmt"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 本文件解析 `etc/hellparty.etc`（深渊派对波次表）—— 本仓以前**从未读取过**的
// 深渊专属怪物表（`_dist/hell-room-two-pillars-diagnosis.md` §2.1b）。
//
// 真档原文（客户端 `Script.pvf`，11799 字节，逐行 dump 见
// `_dist/_hellparty_etc_dump.txt`）：
//
//	[hellparty monster group]
//	[group index]
//	125
//	[group]
//	`A` 1050 0
//	[/group]
//	...
//	[/hellparty monster group]
//
//	[difficulty]
//	4 `A` 8 5 100 100 0 `B` 6 4 0 0 0 `C` 4 3 0 0 0 `D` 0 3 0 0 1
//	[/difficulty]
//
// 解析口径逐条对齐厂商 `GameWorld\HellPartyData.cs`：
//
//   - `:80-94 LoadGroups`：先取 `[hellparty monster group]` 的子节点，再**顺带**扫根节点
//     （真档两种形态都存在过），所以本文件用"进入 section 就记 inGroups"的线性扫法，
//     两种形态都能命中。
//   - `:110-139 ParseSequentialGroupNodes`：`[group index] N` 之后紧跟的 `[group]` 行
//     归到该 N；`ParseFirstInt` 取该 section 里第一个整数。
//   - `:184-221 ParseGroupNode`：`[group]` 体按空白切 token；**单字母 token 切难度**
//     （`char.IsLetter`，本档只出现 A/B/C/D），否则成对读 `(code, entityType)`；
//     `entityType == 1 ? 1 : 0`；难度缺省（出现数字前没见过字母）按 `'A'`。
//   - `:223-263 ParseDifficultyNode`：首 token 是整数就跳过，然后 `字母 + 5 个整数` 一组；
//     本修复只用到 `RewardRollCount`（第 1 个）与 `Probability`（第 4 个，`ResolveManualHellPartyMode`
//     的权重），其余三个按厂商命名保留。
//   - `:295-298 HellModeToGroupKey`：`difficulty == 2 ? 'B' : 'A'`。
//   - `:141-149 ReadHellPartyScript`：读不到文件时厂商**不报错**（返回空表 ⇒ 没有波次）。
//     本目录同样把"读不到/解析失败"降级成空表 + 一条具名诊断事件（见 Service.preloadDungeonWorldMap），
//     绝不因此让整张 worldmap 预载失败。
//
// 缓存口径与 `pvfDungeonMonsterCatalog` / `pvfDungeonAICharacterCatalog` 一致：
// 预载时读一次、建索引，之后只查内存（**不**按请求重读 PVF）。
const currentDungeonHellPartyPVFPath = "etc/hellparty.etc"

var (
	errDungeonHellPartySourceRequired = errors.New("dnf dungeon hell party PVF source is required")
	errDungeonHellPartyTableMissing   = errors.New("dnf etc/hellparty.etc has no [hellparty monster group] section")
)

// dungeonPVFSectionTokens 取一个 section 的 token 副本（`dnfpvf.Section` 的
// `Start`/`End` 是 0 基半开区间，端点越界一律当空段，绝不 panic）。
func dungeonPVFSectionTokens(document *dnfpvf.Document, section dnfpvf.Section) []dnfpvf.Token {
	if document == nil || section.Start < 0 || section.End > len(document.Tokens) || section.Start > section.End {
		return nil
	}
	out := make([]dnfpvf.Token, section.End-section.Start)
	copy(out, document.Tokens[section.Start:section.End])
	return out
}

// pvfDungeonHellPartyGroupEntry 是 `[group]` 里一对 `(code, entityType)`。
type pvfDungeonHellPartyGroupEntry struct {
	Code int64
	// EntityType 是厂商 `HellPartyGroupEntry.EntityType`：1 ⇒ APC（`Type=5`、等级读
	// `AICharacter.lst`），0 ⇒ 普通怪（`Type=0`、等级 = 副本基准等级）。
	EntityType byte
}

// pvfDungeonHellPartyDifficultyRule 是 `[difficulty]` 里一组 `字母 + 5 整数`
// （字段名照厂商 `HellPartyDifficultyRule`）。
type pvfDungeonHellPartyDifficultyRule struct {
	Key              byte
	RewardRollCount  int64
	Unknown1         int64
	RatioProbability int64
	Probability      int64
	Unknown4         int64
}

// pvfDungeonHellPartyCatalog 是 `etc/hellparty.etc` 的只读索引。
type pvfDungeonHellPartyCatalog struct {
	groupKeys []int64
	// entries 的键是组号，值按难度字母（'A'/'B'/…）分组。
	entries map[int64]map[byte][]pvfDungeonHellPartyGroupEntry
	rules   map[byte]pvfDungeonHellPartyDifficultyRule
}

// newPVFDungeonHellPartyCatalog 读并解析 `etc/hellparty.etc`。
func newPVFDungeonHellPartyCatalog(source dnfpvf.Source) (*pvfDungeonHellPartyCatalog, error) {
	if source == nil {
		return nil, errDungeonHellPartySourceRequired
	}
	text, err := source.ReadText(currentDungeonHellPartyPVFPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", currentDungeonHellPartyPVFPath, err)
	}
	document, err := dnfpvf.Parse(currentDungeonHellPartyPVFPath, text)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", currentDungeonHellPartyPVFPath, err)
	}
	return parseDungeonHellPartyCatalog(document)
}

func parseDungeonHellPartyCatalog(document *dnfpvf.Document) (*pvfDungeonHellPartyCatalog, error) {
	if document == nil {
		return nil, errDungeonHellPartyTableMissing
	}
	catalog := &pvfDungeonHellPartyCatalog{
		entries: make(map[int64]map[byte][]pvfDungeonHellPartyGroupEntry),
		rules:   make(map[byte]pvfDungeonHellPartyDifficultyRule),
	}
	inGroups := false
	current := int64(-1)
	sawGroupSection := false
	for _, section := range document.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		switch name {
		case "hellparty monster group":
			inGroups = true
			sawGroupSection = true
			current = -1
			continue
		case "/hellparty monster group":
			inGroups = false
			current = -1
			continue
		case "difficulty":
			for _, rule := range parseDungeonHellPartyDifficultyTokens(dungeonPVFSectionTokens(document, section)) {
				catalog.rules[rule.Key] = rule
			}
			continue
		}
		if !inGroups {
			continue
		}
		switch name {
		case "group index":
			value, ok := firstDungeonHellPartyInt(dungeonPVFSectionTokens(document, section))
			if !ok {
				current = -1
				continue
			}
			current = value
		case "group":
			if current < 0 {
				continue
			}
			group := catalog.entries[current]
			if group == nil {
				group = make(map[byte][]pvfDungeonHellPartyGroupEntry)
				catalog.entries[current] = group
				catalog.groupKeys = append(catalog.groupKeys, current)
			}
			appendDungeonHellPartyGroupTokens(group, dungeonPVFSectionTokens(document, section))
		}
	}
	if !sawGroupSection && len(catalog.entries) == 0 {
		return nil, errDungeonHellPartyTableMissing
	}
	return catalog, nil
}

// appendDungeonHellPartyGroupTokens 逐条对应厂商 `HellPartyData.cs:184-221 ParseGroupNode`。
func appendDungeonHellPartyGroupTokens(group map[byte][]pvfDungeonHellPartyGroupEntry, tokens []dnfpvf.Token) {
	difficulty := byte(0)
	pending := int64(0)
	hasPending := false
	for _, token := range tokens {
		if token.Kind == dnfpvf.TokenSection {
			continue
		}
		value := strings.TrimSpace(token.Value)
		if len(value) == 1 && isDungeonHellPartyLetter(value[0]) {
			difficulty = normalizeDungeonHellPartyDifficulty(value[0])
			hasPending = false
			if _, ok := group[difficulty]; !ok {
				group[difficulty] = nil
			}
			continue
		}
		if token.Kind != dnfpvf.TokenInt {
			continue
		}
		if difficulty == 0 {
			difficulty = 'A'
			if _, ok := group[difficulty]; !ok {
				group[difficulty] = nil
			}
		}
		if !hasPending {
			pending = token.Int
			hasPending = true
			continue
		}
		entityType := byte(0)
		if token.Int == 1 {
			entityType = 1
		}
		group[difficulty] = append(group[difficulty], pvfDungeonHellPartyGroupEntry{Code: pending, EntityType: entityType})
		hasPending = false
	}
}

// parseDungeonHellPartyDifficultyTokens 对应厂商 `HellPartyData.cs:223-263 ParseDifficultyNode`：
// 首 token 是整数就跳过，之后每组 `字母 + 5 个整数` 是一条难度规则（真档一条 `[difficulty]`
// 里同时有 A/B/C/D 四条 ⇒ 必须**返回全部**，否则后面的会盖掉前面的）。
func parseDungeonHellPartyDifficultyTokens(tokens []dnfpvf.Token) []pvfDungeonHellPartyDifficultyRule {
	values := make([]dnfpvf.Token, 0, len(tokens))
	for _, token := range tokens {
		if token.Kind == dnfpvf.TokenSection {
			continue
		}
		if token.Kind == dnfpvf.TokenInt || token.Kind == dnfpvf.TokenString || token.Kind == dnfpvf.TokenIdent {
			values = append(values, token)
		}
	}
	index := 0
	if index < len(values) && values[index].Kind == dnfpvf.TokenInt {
		// 真档 `[difficulty]` 首 token 是 `4`（厂商同样先跳过一个整数）。
		index++
	}
	result := make([]pvfDungeonHellPartyDifficultyRule, 0, 4)
	for index < len(values) {
		keyToken := strings.TrimSpace(values[index].Value)
		index++
		if len(keyToken) != 1 || !isDungeonHellPartyLetter(keyToken[0]) {
			continue
		}
		numbers := make([]int64, 0, 5)
		for index < len(values) && len(numbers) < 5 {
			token := values[index]
			index++
			if token.Kind != dnfpvf.TokenInt {
				continue
			}
			numbers = append(numbers, token.Int)
		}
		if len(numbers) != 5 {
			continue
		}
		result = append(result, pvfDungeonHellPartyDifficultyRule{
			Key:              normalizeDungeonHellPartyDifficulty(keyToken[0]),
			RewardRollCount:  numbers[0],
			Unknown1:         numbers[1],
			RatioProbability: numbers[2],
			Probability:      numbers[3],
			Unknown4:         numbers[4],
		})
	}
	return result
}

func firstDungeonHellPartyInt(tokens []dnfpvf.Token) (int64, bool) {
	for _, token := range tokens {
		if token.Kind == dnfpvf.TokenInt {
			return token.Int, true
		}
	}
	return 0, false
}

func isDungeonHellPartyLetter(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}

func normalizeDungeonHellPartyDifficulty(value byte) byte {
	if value >= 'a' && value <= 'z' {
		return value - ('a' - 'A')
	}
	return value
}

// Count 返回解析到的组数（供预载日志使用）。
func (c *pvfDungeonHellPartyCatalog) Count() int {
	if c == nil {
		return 0
	}
	return len(c.groupKeys)
}

// HasEntries 对应厂商 `HellPartyData.HasEntries`：该组在该难度下有非空怪物表。
func (c *pvfDungeonHellPartyCatalog) HasEntries(groupID int64, difficulty byte) bool {
	return len(c.Entries(groupID, difficulty)) != 0
}

// Entries 对应厂商 `HellPartyData.GetEntries`（返回副本，调用方可安全改）。
func (c *pvfDungeonHellPartyCatalog) Entries(groupID int64, difficulty byte) []pvfDungeonHellPartyGroupEntry {
	if c == nil {
		return nil
	}
	group := c.entries[groupID]
	if group == nil {
		return nil
	}
	entries := group[dungeonHellPartyGroupKey(difficulty)]
	return append([]pvfDungeonHellPartyGroupEntry(nil), entries...)
}

// DifficultyRule 对应厂商 `HellPartyData.GetDifficultyRule`。
func (c *pvfDungeonHellPartyCatalog) DifficultyRule(difficulty byte) (pvfDungeonHellPartyDifficultyRule, bool) {
	if c == nil {
		return pvfDungeonHellPartyDifficultyRule{}, false
	}
	rule, ok := c.rules[dungeonHellPartyGroupKey(difficulty)]
	return rule, ok
}

// dungeonHellPartyGroupKey 对应厂商 `HellPartyData.cs:295-298 HellModeToGroupKey`：
// `difficulty == 2 ? 'B' : 'A'`。
func dungeonHellPartyGroupKey(difficulty byte) byte {
	if difficulty == 2 {
		return 'B'
	}
	return 'A'
}
