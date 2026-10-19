package dnfbridge

import (
	"slices"

	expert "longheng.io/server/internal/modules/dnf/expertjob"
)

// 职业标签来自现用PVF的[usable job]，不从装备中文名称推断。
var currentAuctionCategoryJobs = map[uint16][]string{
	12005: {"[swordman]", "[demonic swordman]"}, 12006: {"[at swordman]"}, 12007: {"[fighter]"}, 12008: {"[at fighter]"}, 12009: {"[gunner]"}, 12010: {"[at gunner]"}, 12011: {"[mage]", "[creator mage]"}, 12012: {"[at mage]"}, 12013: {"[priest]"}, 12017: {"[at priest]"}, 12014: {"[thief]"}, 12015: {"[knight]"}, 12016: {"[demonic lancer]"}, 12018: {"[gun blader]"},
	32002: {"[swordman]"}, 32003: {"[at swordman]"}, 32004: {"[demonic swordman]"}, 32013: {"[at fighter]"}, 32005: {"[fighter]"}, 32006: {"[gunner]"}, 32011: {"[at gunner]"}, 32009: {"[at mage]"}, 32007: {"[mage]"}, 32008: {"[creator mage]"}, 32010: {"[priest]"}, 32016: {"[at priest]"}, 32012: {"[thief]"}, 32014: {"[knight]"}, 32015: {"[demonic lancer]"}, 32017: {"[gun blader]"},
	32101: {"[swordman]"}, 32102: {"[at swordman]"}, 32103: {"[demonic swordman]"}, 32112: {"[at fighter]"}, 32104: {"[fighter]"}, 32105: {"[gunner]"}, 32110: {"[at gunner]"}, 32108: {"[at mage]"}, 32106: {"[mage]"}, 32107: {"[creator mage]"}, 32109: {"[priest]"}, 32115: {"[at priest]"}, 32111: {"[thief]"}, 32113: {"[knight]"}, 32114: {"[demonic lancer]"}, 32116: {"[gun blader]"},
}

var currentAuctionRecipeCategories = map[uint16]uint16{
	31001: 10000, 31101: 11000, 31201: 12000, 31301: 32000, 31302: 32001, 31303: 32100,
	31002: 10101, 31003: 10201, 31004: 10301, 31005: 10401, 31006: 10501, 31007: 10601, 31010: 10801, 31011: 10901,
	31102: 11001, 31103: 11100, 31104: 11200, 31105: 11300, 31106: 11400, 31202: 12001, 31203: 12002, 31204: 12003,
}

func currentAuctionMetadataItemMatches(code uint16, d dungeonDropItemDefinition) bool {
	if jobs, ok := currentAuctionCategoryJobs[code]; ok {
		parent := currentAuctionCategory.nodes[code].parent
		if !currentAuctionCategoryItemMatches(parent, d) {
			return false
		}
		if slices.Contains(d.AuctionUsableJobs, "[all]") {
			return true
		}
		for _, job := range jobs {
			if slices.Contains(d.AuctionUsableJobs, job) {
				return true
			}
		}
		return false
	}
	if target, ok := currentAuctionRecipeCategories[code]; ok {
		return normalizeDungeonDropStackableType(d.StackableType) == "[recipe]" && d.AuctionRecipeOutput != nil && d.AuctionRecipeOutput.Kind == dungeonDropItemEquipment && currentAuctionCategoryItemMatches(target, *d.AuctionRecipeOutput)
	}
	if code == 13004 {
		return d.AuctionExtractor
	}
	if code >= 33001 && code <= 33003 {
		job := map[uint16]byte{33001: expert.AlchemistType, 33002: expert.DollControllerType, 33003: expert.EnchanterType}[code]
		return normalizeDungeonDropStackableType(d.StackableType) == "[material expert job]" && slices.Contains(d.AuctionExpertJobs, job)
	}
	return false
}

func (s *Service) enrichCurrentAuctionCategoryItem(code uint16, c *pvfDungeonDropCatalog, d dungeonDropItemDefinition) (dungeonDropItemDefinition, error) {
	if _, needsOutput := currentAuctionRecipeCategories[code]; needsOutput && d.AuctionRecipeOutputID > 0 {
		output, err := c.ResolveItem(d.AuctionRecipeOutputID)
		if err != nil {
			return d, err
		}
		d.AuctionRecipeOutput = &output
	}
	if d.Kind != dungeonDropItemStackable || (code != 13004 && (code < 33001 || code > 33003)) {
		return d, nil
	}
	s.auctionCategoryExtraOnce.Do(func() {
		catalog, err := s.currentExpertJobCatalog()
		if err != nil {
			s.auctionCategoryExtraErr = err
			return
		}
		s.auctionCategoryExpertJobs = map[uint32][]byte{}
		s.auctionCategoryExtractors = map[uint32]bool{}
		add := func(id int64, job byte) {
			if id <= 0 || id > 0xffffffff {
				return
			}
			key := uint32(id)
			if !slices.Contains(s.auctionCategoryExpertJobs[key], job) {
				s.auctionCategoryExpertJobs[key] = append(s.auctionCategoryExpertJobs[key], job)
			}
		}
		for _, job := range []byte{expert.AlchemistType, expert.DollControllerType, expert.EnchanterType} {
			config, ok := catalog.Config(job)
			if !ok {
				continue
			}
			for _, recipe := range config.Recipes {
				for _, material := range recipe.Materials {
					add(material.ItemID, job)
				}
			}
			for _, rule := range config.ExtractionRules {
				add(rule.ResultItemID, job)
			}
			for _, table := range config.AdditionalResults {
				for _, rule := range table {
					add(rule.ItemID, job)
				}
			}
			for _, table := range config.BigWinResults {
				for _, rule := range table {
					add(rule.ItemID, job)
				}
			}
			for id := range config.Extractors {
				s.auctionCategoryExtractors[uint32(id)] = true
			}
		}
		if enchant, ok := catalog.Enchanter(); ok {
			for id := range enchant.Cards {
				add(id, expert.EnchanterType)
			}
			for _, recipe := range enchant.CardRecipes {
				for _, material := range recipe.Materials {
					add(material.ItemID, expert.EnchanterType)
				}
			}
		}
	})
	if s.auctionCategoryExtraErr != nil {
		return d, s.auctionCategoryExtraErr
	}
	d.AuctionExpertJobs = s.auctionCategoryExpertJobs[d.ItemID]
	d.AuctionExtractor = s.auctionCategoryExtractors[d.ItemID]
	return d, nil
}
