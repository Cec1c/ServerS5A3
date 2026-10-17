package dnfbridge

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 概率与产出只在收到卡片命令时读取现用PVF，避免额外全量索引。
func parseCurrentMonsterCardRules(doc *dnfpvf.Document) (dnfinventory.MonsterCardRules, error) {
	rules := dnfinventory.MonsterCardRules{UpgradePercent: map[int]int{}, MaximumInputs: map[int]int{}, Pools: map[int][]dnfinventory.MonsterCardWeightedReward{}}
	if doc == nil {
		return rules, fmt.Errorf("卡片配置缺失")
	}
	for _, section := range doc.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		if name != "upgrade info" && name != "bind result info" {
			continue
		}
		values := magicBoxSectionInts(doc, section)
		if name == "upgrade info" {
			if len(values) != 2 || values[0] < 0 || values[0] > 255 || values[1] < 0 || values[1] > 100 {
				return rules, fmt.Errorf("卡片升级概率配置无效")
			}
			rarity := int(values[0])
			if _, ok := rules.UpgradePercent[rarity]; ok {
				return rules, fmt.Errorf("重复卡片升级品质%d", rarity)
			}
			rules.UpgradePercent[rarity] = int(values[1])
			continue
		}
		if len(values) < 5 || (len(values)-3)%2 != 0 || values[0] < 0 || values[0] > 3 || values[2] < 2 || values[2] > 5 {
			return rules, fmt.Errorf("卡片合成池配置无效")
		}
		rarity := int(values[0])
		if _, ok := rules.MaximumInputs[rarity]; ok {
			return rules, fmt.Errorf("重复卡片合成品质%d", rarity)
		}
		rules.MaximumInputs[rarity] = int(values[2])
		total := int64(0)
		for i := 3; i < len(values); i += 2 {
			id, weight := values[i], values[i+1]
			if id <= 0 || id > math.MaxUint32 || weight < 0 || weight > math.MaxInt32 || total > math.MaxInt32-weight {
				return rules, fmt.Errorf("卡片合成权重或物品编号无效")
			}
			total += weight
			rules.Pools[rarity] = append(rules.Pools[rarity], dnfinventory.MonsterCardWeightedReward{ItemID: id, Weight: int(weight)})
		}
		if total == 0 {
			return rules, fmt.Errorf("卡片合成池总权重为0")
		}
	}
	if len(rules.UpgradePercent) == 0 || len(rules.Pools) != 4 {
		return rules, fmt.Errorf("卡片升级或合成配置不完整")
	}
	return rules, nil
}

// 每个enchant index只取其第一整数；附魔属性里的整数不属于升级级数。
func currentMonsterCardStages(doc *dnfpvf.Document) []byte {
	if doc == nil {
		return nil
	}
	seen := map[byte]bool{}
	for _, section := range doc.Sections {
		if !strings.EqualFold(strings.TrimSpace(section.Name), "enchant index") {
			continue
		}
		values := magicBoxSectionInts(doc, section)
		if len(values) > 0 && values[0] >= 0 && values[0] <= 255 {
			seen[byte(values[0])] = true
		}
	}
	stages := make([]byte, 0, len(seen))
	for stage := range seen {
		stages = append(stages, stage)
	}
	sort.Slice(stages, func(i, j int) bool { return stages[i] < stages[j] })
	return stages
}

func resolveCurrentMonsterCard(catalog *pvfDungeonDropCatalog, itemID int64) (dnfinventory.MonsterCardDefinition, error) {
	result := dnfinventory.MonsterCardDefinition{ItemID: itemID}
	if catalog == nil || itemID <= 0 || itemID > math.MaxUint32 {
		return result, fmt.Errorf("缺少合法卡片物品目录")
	}
	definition, err := catalog.ResolveItem(uint32(itemID))
	if err != nil {
		return result, err
	}
	if definition.Kind != dungeonDropItemStackable {
		return result, nil
	}
	doc, err := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
	if err != nil {
		return result, err
	}
	result.PVFPath = definition.PVFPath
	result.StackLimit = definition.StackLimit
	result.StackableType = definition.StackableType
	result.Policy = currentPVFItemPlacementPolicy(definition)
	category, _ := doc.Text("item category")
	result.IsCard = strings.EqualFold(strings.Trim(category, "` []"), "monster card")
	if value, ok := doc.Int("rarity"); ok && value >= 0 && value <= 255 {
		result.Rarity = int(value)
	} else if result.IsCard {
		return result, fmt.Errorf("卡片品质缺失：%d", itemID)
	}
	if value, ok := doc.Int("monstercard bind"); ok && value >= 0 && value <= 2 {
		result.IsBinder = true
	}
	if result.IsCard {
		result.Stages = currentMonsterCardStages(doc)
		if len(result.Stages) == 0 || result.Stages[0] != 0 {
			return result, fmt.Errorf("卡片附魔升级表缺失：%d", itemID)
		}
		granted, e := currentPVFItemDefinitionForGrantAt(definition, time.Now().UTC())
		if e != nil {
			return result, e
		}
		result.ExpireAt = granted.ExpirationDate
	}
	return result, nil
}
