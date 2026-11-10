package dnfbridge

import (
	"fmt"
	"math"
	"sync"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// These private sections describe rules for which the original archive has no
// per-encounter field. Ordinary hell rewards use the original sections below.
const privateSpecialEpicSection = "private special epic probabilities"
const privateDemonCountSection = "private demon rift epic counts"
const privateTemplateSection = "private monster template probability"

type privatePVFDropConfig struct {
	special       map[int64]int
	counts        []privatePVFEpicCount
	template      int
	templateFound bool
}
type privatePVFEpicCount struct{ count, weight int }
type privatePVFDropConfigEntry struct {
	config privatePVFDropConfig
	err    error
}

var privatePVFDropConfigs sync.Map
var privatePVFTemplateConfigs sync.Map

func (c *pvfDungeonDropCatalog) privatePVFTemplateProbability() (int, error) {
	if c == nil || c.source == nil {
		return 0, errDungeonDropSourceRequired
	}
	if v, ok := privatePVFTemplateConfigs.Load(c); ok {
		e := v.(privatePVFDropConfigEntry)
		return e.config.template, e.err
	}
	text, err := c.source.ReadText("Etc/ItemDropInfo_Monseter.etc")
	value := 0
	if err == nil {
		var doc *dnfpvf.Document
		doc, err = dnfpvf.Parse("Etc/ItemDropInfo_Monseter.etc", text)
		if err == nil {
			var v []int64
			v, err = privatePVFIntegerSection(doc, privateTemplateSection)
			if err == nil {
				if len(v) != 1 || v[0] < 0 || v[0] > 10000 {
					err = fmt.Errorf("PVF [%s] missing or invalid", privateTemplateSection)
				} else {
					value = int(v[0])
				}
			}
		}
	}
	e := privatePVFDropConfigEntry{config: privatePVFDropConfig{template: value}, err: err}
	actual, _ := privatePVFTemplateConfigs.LoadOrStore(c, e)
	e = actual.(privatePVFDropConfigEntry)
	return e.config.template, e.err
}

func privatePVFIntegerSection(doc *dnfpvf.Document, name string) ([]int64, error) {
	tokens, found := doc.Section(name)
	if !found {
		return nil, nil
	}
	values := make([]int64, 0, len(tokens))
	for _, token := range tokens {
		if token.Kind != dnfpvf.TokenInt {
			return nil, fmt.Errorf("PVF %s requires integers", name)
		}
		values = append(values, token.Int)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("PVF %s is empty", name)
	}
	return values, nil
}

func loadPrivatePVFDropConfig(source dnfpvf.Source) (privatePVFDropConfig, error) {
	c := privatePVFDropConfig{special: map[int64]int{}}
	if source == nil {
		return c, errDungeonDropSourceRequired
	}
	for _, path := range []string{hellPartyRewardDropInfoPath, "Etc/ItemDropInfo_Monseter.etc"} {
		text, err := source.ReadText(path)
		if err != nil {
			return c, err
		}
		doc, err := dnfpvf.Parse(path, text)
		if err != nil {
			return c, err
		}
		if path == hellPartyRewardDropInfoPath {
			v, err := privatePVFIntegerSection(doc, privateSpecialEpicSection)
			if err != nil {
				return c, err
			}
			if len(v)%2 != 0 {
				return c, fmt.Errorf("PVF special epic rows require dungeon/probability pairs")
			}
			for i := 0; i < len(v); i += 2 {
				if _, exists := c.special[v[i]]; exists || v[i] <= 0 || v[i+1] < 0 || v[i+1] > 1000000 {
					return c, fmt.Errorf("invalid PVF special epic row %d", i/2)
				}
				c.special[v[i]] = int(v[i+1])
			}
			v, err = privatePVFIntegerSection(doc, privateDemonCountSection)
			if err != nil {
				return c, err
			}
			if len(v)%2 != 0 {
				return c, fmt.Errorf("PVF demon count rows require count/weight pairs")
			}
			total := int64(0)
			seen := map[int64]bool{}
			for i := 0; i < len(v); i += 2 {
				if seen[v[i]] || v[i] < 0 || v[i] > 32 || v[i+1] < 0 || v[i+1] > 1000000 {
					return c, fmt.Errorf("invalid PVF demon count row %d", i/2)
				}
				seen[v[i]] = true
				total += v[i+1]
				c.counts = append(c.counts, privatePVFEpicCount{int(v[i]), int(v[i+1])})
			}
			if len(v) > 0 && total != 1000000 {
				return c, fmt.Errorf("PVF demon count weights must total 1000000")
			}
		} else {
			v, err := privatePVFIntegerSection(doc, privateTemplateSection)
			if err != nil {
				return c, err
			}
			if len(v) > 0 {
				if len(v) != 1 || v[0] < 0 || v[0] > 10000 {
					return c, fmt.Errorf("PVF monster template probability must be 0..10000")
				}
				c.template = int(v[0])
				c.templateFound = true
			}
		}
	}
	return c, nil
}

func (c *pvfDungeonDropCatalog) privatePVFDropConfig() (privatePVFDropConfig, error) {
	if c == nil {
		return privatePVFDropConfig{}, errDungeonDropSourceRequired
	}
	if value, ok := privatePVFDropConfigs.Load(c); ok {
		e := value.(privatePVFDropConfigEntry)
		return e.config, e.err
	}
	config, err := loadPrivatePVFDropConfig(c.source)
	actual, _ := privatePVFDropConfigs.LoadOrStore(c, privatePVFDropConfigEntry{config, err})
	e := actual.(privatePVFDropConfigEntry)
	return e.config, e.err
}
func (c *pvfDungeonDropCatalog) privateSpecialEpicProbability(id int64) (int, error) {
	config, err := c.privatePVFDropConfig()
	if err != nil {
		return 0, err
	}
	rate, ok := config.special[id]
	if !ok {
		return 0, fmt.Errorf("PVF [%s] missing dungeon %d", privateSpecialEpicSection, id)
	}
	return rate, nil
}

// The native route uses only hellparty.etc roll count, hell drop rates, rarity
// thresholds, item-level references and .equ creation weights. Environment
// rarity filters and the former fixed 13% trial never participate.
func currentNativePVFHellReward(request hellPartyRewardRequest) (hellPartyRewardResult, error) {
	r := hellPartyRewardResult{GroupID: request.GroupID, RewardRollCount: request.RewardRollCount, RateSource: hellPartyRewardDropInfoPath, RarityMode: "pvf", RarityAllowed: hellPartyRewardRarityAllList(), RarityOrder: "pvf", Pool: "equ", EpicWindowSource: "pvf_native_no_extra_window"}
	if request.rng == nil || request.dropCatalog == nil {
		return r, errDungeonHellPartyRewardDropRule
	}
	if request.RewardRollCount < 0 || request.RewardRollCount > 255 {
		return r, fmt.Errorf("PVF hell reward roll count outside 0..255")
	}
	if request.RewardRollCount == 0 {
		return r, nil
	}
	info, err := request.dropCatalog.hellPartyRewardDropInfo()
	if err != nil {
		return r, err
	}
	rate, found := info.rateForLevel(request.DungeonBasisLevel, request.DungeonDifficulty)
	if !found || rate < 0 {
		return r, fmt.Errorf("PVF hell probability missing/invalid at level %d", request.DungeonBasisLevel)
	}
	r.Rate = rate
	if rate == 0 {
		return r, nil
	}
	if rate > 1000 {
		rate = 1000
	}
	r.Chance = int(rate)
	row := int(request.AbyssPartyDifficulty) - 1
	if row < 0 {
		row = 0
	}
	if row >= len(info.RarityThresholds) {
		return r, fmt.Errorf("PVF hell rarity row missing: %d", row)
	}
	thresholds := info.RarityThresholds[row]
	if len(thresholds) < 6 {
		return r, fmt.Errorf("PVF hell rarity row incomplete")
	}
	previous := int64(0)
	for _, v := range thresholds[:6] {
		if v < previous {
			return r, fmt.Errorf("PVF hell rarity thresholds decrease")
		}
		previous = v
	}
	if previous < 1000000 {
		return r, fmt.Errorf("PVF hell rarity thresholds do not cover the random range")
	}
	ref, found := info.GradeRanges[request.DungeonBasisLevel]
	if !found || ref.Down < 0 || ref.Up < 0 {
		return r, fmt.Errorf("PVF hell item level reference missing/invalid")
	}
	low, high := request.DungeonBasisLevel-ref.Down, request.DungeonBasisLevel+ref.Up-1
	if low < 1 {
		low = 1
	}
	if high < low || high > 200 {
		return r, fmt.Errorf("PVF hell item grade range invalid: %d..%d", low, high)
	}
	r.GradeMin, r.GradeMax = low, high
	pool, err := request.dropCatalog.hellPartyRewardEquipmentPool()
	if err != nil {
		return r, err
	}
	band := abyssItemLevelBandFromEnv().forDungeon(request.DungeonMinimumLevel, request.DungeonBasisLevel)
	var candidates [6][]dungeonDropWeightedItem
	for rarity := int64(0); rarity < 6; rarity++ {
		candidates[rarity] = pool.candidatesWithBand(low, high, rarity, band)
		r.RarityCandidates[rarity] = len(candidates[rarity])
	}
	r.RarityCandidatesKnown = true
	for n := int64(0); n < request.RewardRollCount; n++ {
		r.Rolls++
		if request.rng.Next(hellPartyRewardDropChanceDenominator) > r.Chance {
			r.Misses++
			continue
		}
		rarity := info.rarityForRoll(request.AbyssPartyDifficulty, int64(request.rng.Next(1000000))+1)
		r.RarityRolls++
		r.RarityDrawn[rarity]++
		items := candidates[rarity]
		r.CandidateCount = len(items)
		var total uint64
		for _, item := range items {
			total += uint64(item.Weight)
		}
		if total == 0 {
			r.NoDrop++
			r.RarityBucketEmpty++
			continue
		}
		if total > math.MaxUint32 {
			return r, fmt.Errorf("PVF hell equipment weight overflow")
		}
		roll := uint64(request.rng.Next(int(total)))
		for _, item := range items {
			if roll < uint64(item.Weight) {
				r.Selected = append(r.Selected, hellPartyRewardItem{ItemID: item.ItemID, Amount: 1})
				r.RaritySelected[rarity]++
				break
			}
			roll -= uint64(item.Weight)
		}
	}
	return r, nil
}
