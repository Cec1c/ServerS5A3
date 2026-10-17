// Package equipmentcompound 按当前90的真实PVF管理普通粉装、圣物和传说合成。
package equipmentcompound

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"longheng.io/server/internal/modules/dnf/pvf"
)

const (
	RulesPath        = "etc/randomoption/regenerationrandomoption.etc"
	LevelsPath       = "etc/randomoption/randomizedoptionoverall1.etc"
	WeightUnit int64 = 1_000_000
)

var (
	ErrRules      = errors.New("装备合成PVF规则无效或缺失")
	ErrSource     = errors.New("来源装备不符合当前合成规则")
	ErrMaterials  = errors.New("装备合成材料不足或不可用")
	ErrCandidates = errors.New("装备合成合法产出池缺失")
	ErrCapacity   = errors.New("装备合成结果没有已开放的承接格")
	ErrOwner      = errors.New("装备合成角色或账号事务不可用")
)

type Material struct{ ItemID, Count int64 }
type LevelWeights struct{ Same, PlusFive int64 }
type Level struct {
	MinimumLevel int
	Weight       int64
}

// Rules 没有86的固定部位编号、99传承组、传承降权或85级封顶。
// 当前90只有specific材料表；随机全类别模式没有配置时明确拒绝。
type Rules struct {
	Parts       map[uint16]map[string]bool
	Materials   map[int][]Material
	Multipliers map[int]map[int]int64
	Levels      map[int]map[int]LevelWeights
	Excluded    map[int64]bool
	Weights     map[int64]int64
}

func LoadRules(source pvf.Source) (*Rules, error) {
	if source == nil {
		return nil, ErrRules
	}
	main, err := readDocument(source, RulesPath)
	if err != nil {
		return nil, err
	}
	levels, err := readDocument(source, LevelsPath)
	if err != nil {
		return nil, err
	}
	r := &Rules{Parts: map[uint16]map[string]bool{}, Materials: map[int][]Material{}, Multipliers: map[int]map[int]int64{}, Levels: map[int]map[int]LevelWeights{}, Excluded: map[int64]bool{}, Weights: map[int64]int64{}}
	for _, section := range main.Sections {
		if section.Name != "choose part" {
			continue
		}
		t := main.Tokens[section.Start:section.End]
		if len(t) < 3 || t[0].Kind != pvf.TokenInt || t[0].Int < 1 || t[0].Int > 4 || t[1].Kind != pvf.TokenString {
			return nil, fmt.Errorf("%w：90部位组形状", ErrRules)
		}
		part := uint16(t[0].Int)
		if r.Parts[part] != nil {
			return nil, fmt.Errorf("%w：重复部位组", ErrRules)
		}
		r.Parts[part] = map[string]bool{}
		for _, value := range t[2:] {
			if value.Kind != pvf.TokenString || normalize(value.Value) == "" {
				return nil, fmt.Errorf("%w：部位组成员", ErrRules)
			}
			r.Parts[part][normalize(value.Value)] = true
		}
	}
	if len(r.Parts) != 4 {
		return nil, fmt.Errorf("%w：缺少90四种部位组", ErrRules)
	}
	t, err := uniqueSection(main, "compound need material for specific", true)
	if err != nil {
		return nil, err
	}
	for pos := 0; pos < len(t); {
		if pos+2 > len(t) || !integer(t[pos]) || !integer(t[pos+1]) {
			return nil, ErrRules
		}
		group, count := int(t[pos].Int), t[pos+1].Int
		pos += 2
		if !supportedRarity(group) || count <= 0 || count > 32 || int64(len(t)-pos) < count*2 || r.Materials[group] != nil {
			return nil, ErrRules
		}
		seen := map[int64]bool{}
		for i := int64(0); i < count; i++ {
			if !integer(t[pos]) || !integer(t[pos+1]) || t[pos].Int <= 0 || t[pos].Int > math.MaxUint32 || t[pos+1].Int <= 0 || seen[t[pos].Int] {
				return nil, ErrRules
			}
			r.Materials[group] = append(r.Materials[group], Material{t[pos].Int, t[pos+1].Int})
			seen[t[pos].Int] = true
			pos += 2
		}
	}
	t, err = uniqueSection(main, "compound specific apply cost by level", true)
	if err != nil || len(t)%3 != 0 {
		return nil, ErrRules
	}
	for pos := 0; pos < len(t); pos += 3 {
		if !integer(t[pos]) || !integer(t[pos+1]) {
			return nil, ErrRules
		}
		group, level := int(t[pos].Int), int(t[pos+1].Int)
		multiplier, valid := wholeNumber(t[pos+2])
		if !supportedRarity(group) || level < 1 || level > math.MaxInt16 || !valid || multiplier < 1 || multiplier > math.MaxInt32 {
			return nil, ErrRules
		}
		if r.Multipliers[group] == nil {
			r.Multipliers[group] = map[int]int64{}
		}
		if _, duplicate := r.Multipliers[group][level]; duplicate {
			return nil, ErrRules
		}
		r.Multipliers[group][level] = multiplier
	}
	t, err = uniqueSection(levels, "regen level limit", true)
	if err != nil || len(t)%4 != 0 {
		return nil, ErrRules
	}
	for pos := 0; pos < len(t); pos += 4 {
		for _, value := range t[pos : pos+4] {
			if !integer(value) {
				return nil, ErrRules
			}
		}
		group, level := int(t[pos].Int), int(t[pos+1].Int)
		a, b := t[pos+2].Int, t[pos+3].Int
		if !supportedRarity(group) || level < 1 || level > math.MaxInt16 || a < 0 || b < 0 || a > math.MaxInt64-b || a+b == 0 {
			return nil, ErrRules
		}
		if r.Levels[group] == nil {
			r.Levels[group] = map[int]LevelWeights{}
		}
		if _, duplicate := r.Levels[group][level]; duplicate {
			return nil, ErrRules
		}
		r.Levels[group][level] = LevelWeights{a, b}
	}
	for _, group := range []int{3, 6} {
		if len(r.Materials[group]) == 0 || len(r.Multipliers[group]) == 0 || len(r.Levels[group]) == 0 {
			return nil, ErrRules
		}
	}
	t, err = uniqueSection(main, "except item index", false)
	if err != nil {
		return nil, err
	}
	for _, value := range t {
		if !integer(value) || value.Int <= 0 || value.Int > math.MaxUint32 {
			return nil, ErrRules
		}
		r.Excluded[value.Int] = true
	}
	t, err = uniqueSection(main, "probability exception handling", false)
	if err != nil || len(t)%2 != 0 {
		return nil, ErrRules
	}
	for pos := 0; pos < len(t); pos += 2 {
		if !integer(t[pos]) || t[pos].Int <= 0 || t[pos].Int > math.MaxUint32 {
			return nil, ErrRules
		}
		weight, valid := scaledWeight(t[pos+1])
		if !valid {
			return nil, ErrRules
		}
		if _, duplicate := r.Weights[t[pos].Int]; duplicate {
			return nil, ErrRules
		}
		r.Weights[t[pos].Int] = weight
	}
	return r, nil
}

func (r *Rules) Requirements(rarity, level int) ([]Material, error) {
	if r == nil || !supportedRarity(rarity) {
		return nil, ErrRules
	}
	key := floorLevel(r.Multipliers[rarity], level)
	if key < 1 || len(r.Materials[rarity]) == 0 {
		return nil, ErrRules
	}
	multiplier := r.Multipliers[rarity][key]
	result := make([]Material, 0, len(r.Materials[rarity]))
	for _, material := range r.Materials[rarity] {
		if multiplier <= 0 || material.Count <= 0 || material.Count > math.MaxInt64/multiplier {
			return nil, ErrRules
		}
		result = append(result, Material{material.ItemID, material.Count * multiplier})
	}
	return result, nil
}

func (r *Rules) TargetLevels(rarity, level int) ([]Level, error) {
	if r == nil || !supportedRarity(rarity) {
		return nil, ErrRules
	}
	table := r.Levels[rarity]
	maximum := 0
	for candidate := range table {
		if candidate > maximum {
			maximum = candidate
		}
	}
	key := floorLevel(table, level)
	if key < 1 || level > maximum {
		return nil, ErrSource
	}
	w := table[key]
	result := []Level{}
	if w.Same > 0 {
		result = append(result, Level{level, w.Same})
	}
	if w.PlusFive > 0 && level+5 <= maximum {
		result = append(result, Level{level + 5, w.PlusFive})
	}
	if len(result) == 0 {
		return nil, ErrRules
	}
	return result, nil
}

func (r *Rules) MaterialIDs() []int64 {
	ids := map[int64]bool{}
	for _, materials := range r.Materials {
		for _, material := range materials {
			ids[material.ItemID] = true
		}
	}
	result := make([]int64, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func floorLevel[T any](table map[int]T, level int) int {
	best := 0
	for candidate := range table {
		if candidate <= level && candidate > best {
			best = candidate
		}
	}
	return best
}

func supportedRarity(value int) bool { return value == 3 || value == 6 }
func normalize(value string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(value), "[]`"))
}
func integer(token pvf.Token) bool { return token.Kind == pvf.TokenInt }
func number(token pvf.Token) (float64, bool) {
	switch token.Kind {
	case pvf.TokenInt:
		return float64(token.Int), true
	case pvf.TokenFloat:
		return token.Float, !math.IsInf(token.Float, 0) && !math.IsNaN(token.Float)
	}
	return 0, false
}
func wholeNumber(token pvf.Token) (int64, bool) {
	value, valid := number(token)
	if !valid || value < 0 || value > math.MaxInt32 || math.Trunc(value) != value {
		return 0, false
	}
	return int64(value), true
}
func scaledWeight(token pvf.Token) (int64, bool) {
	value, valid := number(token)
	if !valid || value <= 0 || value > 1000 {
		return 0, false
	}
	scaled := value * float64(WeightUnit)
	rounded := math.Round(scaled)
	if rounded < 1 || math.Abs(scaled-rounded) > 1e-6 {
		return 0, false
	}
	return int64(rounded), true
}
func uniqueSection(doc *pvf.Document, name string, required bool) ([]pvf.Token, error) {
	var tokens []pvf.Token
	found := false
	for _, section := range doc.Sections {
		if section.Name != name {
			continue
		}
		if found {
			return nil, fmt.Errorf("%w：重复%s", ErrRules, name)
		}
		found = true
		tokens = doc.Tokens[section.Start:section.End]
	}
	if required && (!found || len(tokens) == 0) {
		return nil, fmt.Errorf("%w：缺少%s", ErrRules, name)
	}
	return tokens, nil
}
func readDocument(source pvf.Source, docPath string) (*pvf.Document, error) {
	text, err := source.ReadText(docPath)
	if err != nil {
		return nil, fmt.Errorf("%w：读取%s：%v", ErrRules, docPath, err)
	}
	doc, err := pvf.Parse(docPath, text)
	if err != nil {
		return nil, fmt.Errorf("%w：解析%s：%v", ErrRules, docPath, err)
	}
	return doc, nil
}
