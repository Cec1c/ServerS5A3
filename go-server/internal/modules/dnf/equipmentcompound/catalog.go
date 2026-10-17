package equipmentcompound

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"longheng.io/server/internal/modules/dnf/pvf"
)

type Definition struct {
	ItemID                                                    int64
	PVFPath, Name, Group, Category, EquipmentType, AttachType string
	Rarity, MinimumLevel                                      int
	CreationRate                                              int64
	HasCreationRate                                           bool
	GeneratedOption, Temporary                                bool
}

func (d Definition) Legacy() bool { return d.Category == "legacy" }

type Candidate struct {
	Definition Definition
	Weight     int64
}
type Pool struct {
	MinimumLevel int
	Weight       int64
	Candidates   []Candidate
}
type Statistics struct{ References, Parsed, UnsupportedMetadata, Unreadable, Eligible int }
type bucketKey struct {
	Rarity, Level int
	Part          uint16
}

type Catalog struct {
	Rules       *Rules
	source      pvf.Source
	refs        []pvf.ListEntry
	mu          sync.Mutex
	loaded      bool
	definitions map[int64]Definition
	buckets     map[bucketKey][]Candidate
	statistics  Statistics
}

func NewCatalog(source pvf.Source) (*Catalog, error) {
	rules, err := LoadRules(source)
	if err != nil {
		return nil, err
	}
	doc, err := readDocument(source, "equipment/equipment.lst")
	if err != nil {
		return nil, err
	}
	refs := pvf.ParseList(doc)
	if len(refs) == 0 {
		return nil, ErrCandidates
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	return &Catalog{Rules: rules, source: source, refs: refs}, nil
}

// Preload 在玩家资产事务之前完成只读建池。中途取消时不发布半份目录。
// 每个真实物品ID只登记一次；无法读取的定义不进入候选，来源查询也会拒绝。
func (c *Catalog) Preload(ctx context.Context) (Statistics, error) {
	if c == nil || c.source == nil || c.Rules == nil {
		return Statistics{}, ErrRules
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Statistics{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		return c.statistics, nil
	}
	definitions := map[int64]Definition{}
	buckets := map[bucketKey][]Candidate{}
	stats := Statistics{References: len(c.refs)}
	for _, entry := range c.refs {
		if err := ctx.Err(); err != nil {
			return Statistics{}, err
		}
		if entry.ID <= 0 || entry.ID > 0xffffffff {
			continue
		}
		if _, duplicate := definitions[entry.ID]; duplicate {
			return Statistics{}, fmt.Errorf("%w：重复装备ID%d", ErrRules, entry.ID)
		}
		definition, err := c.readEquipment(entry)
		if err != nil {
			if errors.Is(err, ErrSource) {
				stats.UnsupportedMetadata++
			} else {
				stats.Unreadable++
			}
			continue
		}
		stats.Parsed++
		if !supportedRarity(definition.Rarity) {
			continue
		}
		definitions[entry.ID] = definition
		if !c.Rules.eligible(definition) {
			continue
		}
		weight := WeightUnit
		if exceptional, found := c.Rules.Weights[entry.ID]; found {
			weight = exceptional
		}
		part, _ := c.Rules.partForGroup(definition.Group)
		key := bucketKey{definition.Rarity, definition.MinimumLevel, part}
		buckets[key] = append(buckets[key], Candidate{definition, weight})
		stats.Eligible++
	}
	if stats.Eligible == 0 {
		return Statistics{}, ErrCandidates
	}
	c.definitions, c.buckets, c.statistics, c.loaded = definitions, buckets, stats, true
	return stats, nil
}

// Definitions 返回可合成常规装备的只读副本，便于目录核对与能力检查。
func (c *Catalog) Definitions() []Definition {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]Definition, 0, c.statistics.Eligible)
	for _, definition := range c.definitions {
		if c.Rules.eligible(definition) {
			result = append(result, definition)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ItemID < result[j].ItemID })
	return result
}

func (c *Catalog) Resolve(itemID int64) (Definition, error) {
	if c == nil {
		return Definition{}, ErrRules
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		return Definition{}, ErrCandidates
	}
	d, exists := c.definitions[itemID]
	if !exists || !c.Rules.eligible(d) {
		return Definition{}, ErrSource
	}
	return d, nil
}

func (c *Catalog) Pools(source Definition, part uint16) ([]Pool, error) {
	if c == nil || c.Rules == nil || !c.Rules.eligible(source) || !c.Rules.Parts[part][source.Group] {
		return nil, ErrSource
	}
	levels, err := c.Rules.TargetLevels(source.Rarity, source.MinimumLevel)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		return nil, ErrCandidates
	}
	pools := make([]Pool, 0, len(levels))
	for _, level := range levels {
		// 90只按PVF四大类随机，不限制职业、武器细类、防具甲种或具体部位。
		// 圣物和普通神器同为品质3，使用同一产出池。
		key := bucketKey{source.Rarity, level.MinimumLevel, part}
		candidates := c.buckets[key]
		if len(candidates) == 0 && level.MinimumLevel == source.MinimumLevel {
			// 同级缺池时保留PVF等级权重；抽中同级仍走正常建装流程，
			// 只复用已加载的来源模板，不保留旧装备实例及其强化、附魔。
			if cached, found := c.definitions[source.ItemID]; found && cached == source {
				weight := WeightUnit
				if exceptional, found := c.Rules.Weights[cached.ItemID]; found {
					weight = exceptional
				}
				candidates = []Candidate{{cached, weight}}
			}
		}
		if len(candidates) == 0 {
			// 90存在30级传说、却没有合法35级同大类的稀疏目录。
			// 同级池存在时允许只合成同级，不生成体验装或跨品质模板；
			// 无法从已加载的合法来源模板补齐同级候选时仍拒绝。
			if level.MinimumLevel == source.MinimumLevel+5 && len(pools) > 0 {
				continue
			}
			return nil, fmt.Errorf("%w：品质%d等级%d大类%d", ErrCandidates, source.Rarity, level.MinimumLevel, part)
		}
		pools = append(pools, Pool{level.MinimumLevel, level.Weight, append([]Candidate(nil), candidates...)})
	}
	return pools, nil
}

func (r *Rules) eligible(d Definition) bool {
	if r == nil || d.ItemID <= 0 || !supportedRarity(d.Rarity) || d.MinimumLevel < 1 || d.AttachType != "sealing" || d.GeneratedOption || d.Temporary || r.Excluded[d.ItemID] {
		return false
	}
	if _, known := r.partForGroup(d.Group); !known {
		return false
	}
	if _, err := r.TargetLevels(d.Rarity, d.MinimumLevel); err != nil {
		return false
	}
	// 普通神器和有掉落字段的圣物使用实际正生成率；圣物缺字段才保留特例。
	// 传说为独立掉落，通用生成率均为0，不能据此排空传说池；名称不作为准入条件。
	return d.Rarity == 6 || d.CreationRate > 0 || d.Legacy() && !d.HasCreationRate
}

func (c *Catalog) readEquipment(entry pvf.ListEntry) (Definition, error) {
	reference := strings.Trim(strings.ReplaceAll(strings.TrimSpace(entry.Path), "\\", "/"), "/")
	if !strings.HasPrefix(strings.ToLower(reference), "equipment/") {
		reference = "equipment/" + reference
	}
	docPath := reference
	text, err := c.source.ReadText(docPath)
	if err != nil {
		dir, base := path.Split(reference)
		if !strings.HasPrefix(strings.ToLower(base), "(r)") {
			docPath = dir + "(r)" + base
			text, err = c.source.ReadText(docPath)
		}
	}
	if err != nil {
		return Definition{}, err
	}
	doc, err := pvf.Parse(docPath, text)
	if err != nil {
		return Definition{}, err
	}
	d := Definition{ItemID: entry.ID, PVFPath: docPath}
	d.Name, _ = doc.Text("name")
	d.Group, _ = doc.Text("item group name")
	d.Group = normalize(d.Group)
	d.Category, _ = doc.Text("item category")
	d.Category = normalize(d.Category)
	d.EquipmentType, _ = doc.Text("equipment type")
	d.EquipmentType = normalize(d.EquipmentType)
	d.AttachType, _ = doc.Text("attach type")
	d.AttachType = normalize(d.AttachType)
	rarity, rarityFound := doc.Int("rarity")
	level, levelFound := doc.Int("minimum level")
	if !rarityFound || !levelFound || rarity < 0 || rarity > 255 || level < 1 || level > 32767 {
		return Definition{}, ErrSource
	}
	d.Rarity, d.MinimumLevel = int(rarity), int(level)
	d.CreationRate, d.HasCreationRate = doc.Int("creation rate")
	randomOption, _ := doc.Int("random option")
	// Force Result Item Rule=1 0同样存在于合法普通神器，不能单独当作假粉标记。
	d.GeneratedOption = d.Rarity == 3 && randomOption > 0
	if deadline, found := doc.Text("expiration date"); found && strings.TrimSpace(deadline) != "" {
		d.Temporary = true
	}
	if period, found := doc.Int("usable period"); found && period > 0 {
		d.Temporary = true
	}
	if daily, found := doc.Int("daily delete item"); found && daily == 1 {
		d.Temporary = true
	}
	return d, nil
}

func (r *Rules) partForGroup(group string) (uint16, bool) {
	var part uint16
	for id, groups := range r.Parts {
		if groups[group] {
			if part != 0 {
				return 0, false
			}
			part = id
		}
	}
	return part, part != 0
}
