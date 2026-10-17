package dnfbridge

// 依据本机90参考版本迁入；装备、材料、档位和排除表均读取当前PVF。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	currentDungeonDimensionGatePath        = "Etc/DimensionGate.etc"
	currentDungeonDimensionInfoPath        = "Etc/DungeonEtc/DimensionDungeonInfo.etc"
	currentDungeonDimensionDropListPath    = "Etc/DimensionGateDropList.etc"
	currentDungeonDimensionDropExcludePath = "Etc/DimensionGateDropList_Exclude.etc"
	currentDungeonClearRewardRulePath      = "Etc/ItemDropInfo_ClearReward.etc"

	// The same-version reference server adds an area material to the ordinary
	// monster-template pool with weight 100.  The weight only selects within
	// the pool; the existing 1000/10000 outer roll remains the drop rate.
	currentDungeonDimensionMaterialWeight uint32 = 100
)

var errCurrentDungeonDimensionDropInvalid = errors.New("异界掉落PVF配置无效")

type currentDungeonDimensionDropEntry struct {
	Group  int
	ItemID uint32
}

type currentDungeonDimensionLevelThreshold struct {
	Tier         int
	MinimumLevel int
}

type currentDungeonDimensionChronicleKey struct {
	Job      byte
	GrowType byte
}

type currentDungeonDimensionChroniclePool struct {
	Normal []uint32
	Set    []uint32
}

// currentDungeonDimensionChronicleRate is the exact three-column row from
// ItemDropInfo_ClearReward.etc [chronicle set reward rate].  RewardPercent is
// used by the clear-card path. SetPercent chooses the rarer set pool after a
// monster's ordinary PVF equipment roll has already succeeded.
type currentDungeonDimensionChronicleRate struct {
	RewardPercent int
	SetPercent    int
}

type currentDungeonDimensionDropCatalog struct {
	LegacyDungeons    map[int64]struct{}
	ExactTiers        map[int64]int
	LevelThresholds   []currentDungeonDimensionLevelThreshold
	ItemsByTier       map[int][]currentDungeonDimensionDropEntry
	ChronicleByCareer map[currentDungeonDimensionChronicleKey]currentDungeonDimensionChroniclePool
	ChronicleRates    map[int]currentDungeonDimensionChronicleRate
}

type currentDungeonDimensionDropCacheEntry struct {
	catalog *currentDungeonDimensionDropCatalog
	err     error
}

var currentDungeonDimensionDropsByMonsterCatalog sync.Map

// 组队职业掉落只读取领取者自己的角色，缺失时拒绝该成员的本次规划。
func (s *Service) loadDimensionDropCharacter(characterID uint16) (dnfrepo.CharacterRecord, error) {
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil {
		return dnfrepo.CharacterRecord{}, errCurrentDungeonDimensionDropInvalid
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	character, found, err := repositories.Character.Load(ctx, strconv.Itoa(int(characterID)))
	if err != nil {
		return dnfrepo.CharacterRecord{}, err
	}
	if !found {
		return dnfrepo.CharacterRecord{}, fmt.Errorf("异界掉落领取角色不存在：%d", characterID)
	}
	return character, nil
}

func currentDungeonRandomDimensionCardItem(rng *currentDungeonDropLCG, items *pvfDungeonDropCatalog, dimension *currentDungeonDimensionDropCatalog, dungeonID int64, level int, job byte, growType byte, now time.Time) (dungeonCardItemReward, bool, error) {
	_, itemID, _, matched, selected, err := currentDungeonDimensionChronicleItem(dimension, dungeonID, level, job, growType, rng, true)
	if err != nil || !selected {
		return dungeonCardItemReward{}, false, err
	}
	if !matched {
		return dungeonCardItemReward{}, false, errCurrentDungeonDimensionDropInvalid
	}
	item, err := currentDungeonCardItemReward(rng, items, itemID, now)
	return item, err == nil, err
}

func currentDungeonDimensionDrops(catalog *pvfDungeonMonsterCatalog) (*currentDungeonDimensionDropCatalog, error) {
	if catalog == nil || catalog.source == nil {
		return nil, errDungeonDropSourceRequired
	}
	if cached, found := currentDungeonDimensionDropsByMonsterCatalog.Load(catalog); found {
		entry := cached.(currentDungeonDimensionDropCacheEntry)
		return entry.catalog, entry.err
	}
	loaded, err := loadCurrentDungeonDimensionDrops(catalog.source)
	entry := currentDungeonDimensionDropCacheEntry{catalog: loaded, err: err}
	actual, _ := currentDungeonDimensionDropsByMonsterCatalog.LoadOrStore(catalog, entry)
	entry = actual.(currentDungeonDimensionDropCacheEntry)
	return entry.catalog, entry.err
}

func loadCurrentDungeonDimensionDrops(source dnfpvf.Source) (*currentDungeonDimensionDropCatalog, error) {
	if source == nil {
		return nil, errDungeonDropSourceRequired
	}
	catalog := &currentDungeonDimensionDropCatalog{
		LegacyDungeons:    make(map[int64]struct{}),
		ExactTiers:        make(map[int64]int),
		ItemsByTier:       make(map[int][]currentDungeonDimensionDropEntry),
		ChronicleByCareer: make(map[currentDungeonDimensionChronicleKey]currentDungeonDimensionChroniclePool),
		ChronicleRates:    make(map[int]currentDungeonDimensionChronicleRate),
	}

	gateText, err := source.ReadText(currentDungeonDimensionGatePath)
	if err != nil && !currentDungeonDimensionDropDocumentMissing(err) {
		return nil, fmt.Errorf("read %s: %w", currentDungeonDimensionGatePath, err)
	}
	if strings.TrimSpace(gateText) != "" {
		document, parseErr := dnfpvf.Parse(currentDungeonDimensionGatePath, gateText)
		if parseErr != nil {
			return nil, parseErr
		}
		values, sectionErr := currentDungeonDimensionSectionInts(document, "dimensiondungeon list", false)
		if sectionErr != nil {
			return nil, sectionErr
		}
		if len(values)%3 != 0 {
			return nil, fmt.Errorf("%w: %s [dimensiondungeon list] values=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonDimensionGatePath, len(values))
		}
		for index := 0; index < len(values); index += 3 {
			dungeonID := values[index]
			if dungeonID <= 0 {
				return nil, fmt.Errorf("%w: %s dungeon_id=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonDimensionGatePath, dungeonID)
			}
			catalog.LegacyDungeons[dungeonID] = struct{}{}
		}
	}

	infoText, err := source.ReadText(currentDungeonDimensionInfoPath)
	if err != nil && !currentDungeonDimensionDropDocumentMissing(err) {
		return nil, fmt.Errorf("read %s: %w", currentDungeonDimensionInfoPath, err)
	}
	if strings.TrimSpace(infoText) == "" {
		return catalog, nil
	}
	document, err := dnfpvf.Parse(currentDungeonDimensionInfoPath, infoText)
	if err != nil {
		return nil, err
	}
	infoValues, err := currentDungeonDimensionSectionInts(document, "dimension dungeon info", false)
	if err != nil {
		return nil, err
	}
	if len(infoValues)%2 != 0 {
		return nil, fmt.Errorf("%w: %s [dimension dungeon info] values=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonDimensionInfoPath, len(infoValues))
	}
	for index := 0; index < len(infoValues); index += 2 {
		tier, dungeonID := infoValues[index], infoValues[index+1]
		if tier < 0 || tier > int64(^uint8(0)) || dungeonID <= 0 {
			return nil, fmt.Errorf("%w: %s tier=%d dungeon_id=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonDimensionInfoPath, tier, dungeonID)
		}
		if _, duplicate := catalog.ExactTiers[dungeonID]; duplicate {
			return nil, fmt.Errorf("%w: %s duplicate dungeon_id=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonDimensionInfoPath, dungeonID)
		}
		catalog.ExactTiers[dungeonID] = int(tier)
	}

	thresholdValues, err := currentDungeonDimensionSectionInts(document, "dungeon difficulty condition info", false)
	if err != nil {
		return nil, err
	}
	if len(thresholdValues)%2 != 0 {
		return nil, fmt.Errorf("%w: %s [dungeon difficulty condition info] values=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonDimensionInfoPath, len(thresholdValues))
	}
	for index := 0; index < len(thresholdValues); index += 2 {
		tier, level := thresholdValues[index], thresholdValues[index+1]
		if tier < 0 || tier > int64(^uint8(0)) || level < 0 || level > int64(^uint8(0)) {
			return nil, fmt.Errorf("%w: %s threshold tier=%d level=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonDimensionInfoPath, tier, level)
		}
		catalog.LevelThresholds = append(catalog.LevelThresholds, currentDungeonDimensionLevelThreshold{Tier: int(tier), MinimumLevel: int(level)})
	}
	sort.SliceStable(catalog.LevelThresholds, func(i, j int) bool {
		if catalog.LevelThresholds[i].MinimumLevel == catalog.LevelThresholds[j].MinimumLevel {
			return catalog.LevelThresholds[i].Tier < catalog.LevelThresholds[j].Tier
		}
		return catalog.LevelThresholds[i].MinimumLevel < catalog.LevelThresholds[j].MinimumLevel
	})

	dropValues, err := currentDungeonDimensionSectionInts(document, "drop item Info", false)
	if err != nil {
		return nil, err
	}
	if len(dropValues)%3 != 0 {
		return nil, fmt.Errorf("%w: %s [drop item Info] values=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonDimensionInfoPath, len(dropValues))
	}
	for index := 0; index < len(dropValues); index += 3 {
		tier, group, itemID := dropValues[index], dropValues[index+1], dropValues[index+2]
		if tier < 0 || tier > int64(^uint8(0)) || group < 0 || group > int64(^uint8(0)) || itemID <= 0 || itemID > int64(^uint32(0)) {
			return nil, fmt.Errorf("%w: %s drop tier=%d group=%d item_id=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonDimensionInfoPath, tier, group, itemID)
		}
		catalog.ItemsByTier[int(tier)] = append(catalog.ItemsByTier[int(tier)], currentDungeonDimensionDropEntry{Group: int(group), ItemID: uint32(itemID)})
	}
	if err := loadCurrentDungeonDimensionChronicleDrops(source, catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

func loadCurrentDungeonDimensionChronicleDrops(source dnfpvf.Source, catalog *currentDungeonDimensionDropCatalog) error {
	if source == nil || catalog == nil {
		return errDungeonDropSourceRequired
	}
	dropText, err := source.ReadText(currentDungeonDimensionDropListPath)
	if err != nil {
		if currentDungeonDimensionDropDocumentMissing(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", currentDungeonDimensionDropListPath, err)
	}
	if strings.TrimSpace(dropText) == "" {
		return nil
	}

	excluded := make(map[uint32]struct{})
	excludeText, err := source.ReadText(currentDungeonDimensionDropExcludePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", currentDungeonDimensionDropExcludePath, err)
	}
	excludeDocument, err := dnfpvf.Parse(currentDungeonDimensionDropExcludePath, excludeText)
	if err != nil {
		return err
	}
	excludeValues, err := currentDungeonDimensionSectionInts(excludeDocument, "exclude list", false)
	if err != nil {
		return err
	}
	for _, itemID := range excludeValues {
		if itemID <= 0 || itemID > int64(^uint32(0)) {
			return fmt.Errorf("%w: %s item_id=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonDimensionDropExcludePath, itemID)
		}
		excluded[uint32(itemID)] = struct{}{}
	}

	document, err := dnfpvf.Parse(currentDungeonDimensionDropListPath, dropText)
	if err != nil {
		return err
	}
	for sectionIndex := 0; sectionIndex < len(document.Sections); sectionIndex++ {
		section := document.Sections[sectionIndex]
		if !strings.EqualFold(strings.TrimSpace(section.Name), "chronicle grow type") {
			continue
		}
		careerValues := magicBoxSectionInts(document, section)
		if len(careerValues) != 2 || careerValues[0] < 0 || careerValues[0] > int64(^uint8(0)) || careerValues[1] < 0 || careerValues[1] > int64(^uint8(0)) {
			return fmt.Errorf("%w: %s section=%d career=%v", errCurrentDungeonDimensionDropInvalid, currentDungeonDimensionDropListPath, sectionIndex, careerValues)
		}
		key := currentDungeonDimensionChronicleKey{Job: byte(careerValues[0]), GrowType: byte(careerValues[1])}
		if _, duplicate := catalog.ChronicleByCareer[key]; duplicate {
			return fmt.Errorf("%w: %s duplicate job=%d grow_type=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonDimensionDropListPath, key.Job, key.GrowType)
		}
		pool := currentDungeonDimensionChroniclePool{}
		normalFound, setFound, closed := false, false, false
		for nestedIndex := sectionIndex + 1; nestedIndex < len(document.Sections); nestedIndex++ {
			nested := document.Sections[nestedIndex]
			name := strings.ToLower(strings.TrimSpace(nested.Name))
			if name == "/chronicle grow type" {
				sectionIndex = nestedIndex
				closed = true
				break
			}
			var destination *[]uint32
			switch name {
			case "normal chronicle list":
				if normalFound {
					return fmt.Errorf("%w: %s duplicate normal list job=%d grow_type=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonDimensionDropListPath, key.Job, key.GrowType)
				}
				normalFound = true
				destination = &pool.Normal
			case "set chronicle list":
				if setFound {
					return fmt.Errorf("%w: %s duplicate set list job=%d grow_type=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonDimensionDropListPath, key.Job, key.GrowType)
				}
				setFound = true
				destination = &pool.Set
			default:
				continue
			}
			for _, itemID := range magicBoxSectionInts(document, nested) {
				if itemID <= 0 || itemID > int64(^uint32(0)) {
					return fmt.Errorf("%w: %s job=%d grow_type=%d item_id=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonDimensionDropListPath, key.Job, key.GrowType, itemID)
				}
				id := uint32(itemID)
				if _, blocked := excluded[id]; blocked {
					continue
				}
				*destination = append(*destination, id)
			}
		}
		if !closed || !normalFound || !setFound {
			return fmt.Errorf("%w: %s incomplete job=%d grow_type=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonDimensionDropListPath, key.Job, key.GrowType)
		}
		catalog.ChronicleByCareer[key] = pool
	}
	if len(catalog.ChronicleByCareer) == 0 {
		return fmt.Errorf("%w: %s has no career rows", errCurrentDungeonDimensionDropInvalid, currentDungeonDimensionDropListPath)
	}

	rateText, err := source.ReadText(currentDungeonClearRewardRulePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", currentDungeonClearRewardRulePath, err)
	}
	rateDocument, err := dnfpvf.Parse(currentDungeonClearRewardRulePath, rateText)
	if err != nil {
		return err
	}
	rateValues, err := currentDungeonDimensionSectionInts(rateDocument, "chronicle set reward rate", false)
	if err != nil {
		return err
	}
	if len(rateValues)%3 != 0 {
		return fmt.Errorf("%w: %s [chronicle set reward rate] values=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonClearRewardRulePath, len(rateValues))
	}
	for index := 0; index < len(rateValues); index += 3 {
		tier, rewardPercent, setPercent := rateValues[index], rateValues[index+1], rateValues[index+2]
		if tier < 0 || tier > int64(^uint8(0)) || rewardPercent < 0 || rewardPercent > 100 || setPercent < 0 || setPercent > 100 {
			return fmt.Errorf("%w: %s tier=%d reward=%d set=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonClearRewardRulePath, tier, rewardPercent, setPercent)
		}
		if _, duplicate := catalog.ChronicleRates[int(tier)]; duplicate {
			return fmt.Errorf("%w: %s duplicate tier=%d", errCurrentDungeonDimensionDropInvalid, currentDungeonClearRewardRulePath, tier)
		}
		catalog.ChronicleRates[int(tier)] = currentDungeonDimensionChronicleRate{RewardPercent: int(rewardPercent), SetPercent: int(setPercent)}
	}
	return nil
}

func currentDungeonDimensionTier(catalog *currentDungeonDimensionDropCatalog, dungeonID int64, characterLevel int) (int, bool) {
	if catalog == nil || dungeonID <= 0 {
		return 0, false
	}
	tier, matched := catalog.ExactTiers[dungeonID]
	if matched {
		return tier, true
	}
	if _, matched = catalog.LegacyDungeons[dungeonID]; !matched {
		return 0, false
	}
	for _, threshold := range catalog.LevelThresholds {
		if characterLevel < threshold.MinimumLevel {
			break
		}
		tier = threshold.Tier
	}
	return tier, true
}

func currentDungeonDimensionDropPool(
	catalog *currentDungeonDimensionDropCatalog,
	dungeonID int64,
	characterLevel int,
) (int, []dungeonMonsterDropPoolEntry, bool, error) {
	if catalog == nil || dungeonID <= 0 {
		return 0, nil, false, nil
	}
	tier, matched := currentDungeonDimensionTier(catalog, dungeonID, characterLevel)
	if !matched {
		return 0, nil, false, nil
	}
	selectedTier := -1
	for candidate := range catalog.ItemsByTier {
		if candidate <= tier && candidate > selectedTier && len(catalog.ItemsByTier[candidate]) > 0 {
			selectedTier = candidate
		}
	}
	if selectedTier < 0 {
		return tier, nil, true, fmt.Errorf("%w: dungeon_id=%d tier=%d has no drop-item row", errCurrentDungeonDimensionDropInvalid, dungeonID, tier)
	}
	entries := catalog.ItemsByTier[selectedTier]
	pool := make([]dungeonMonsterDropPoolEntry, 0, len(entries))
	for _, entry := range entries {
		pool = append(pool, dungeonMonsterDropPoolEntry{
			ItemID: entry.ItemID,
			Weight: currentDungeonDimensionMaterialWeight,
		})
	}
	return selectedTier, pool, true, nil
}

func currentDungeonDimensionChronicleItem(
	catalog *currentDungeonDimensionDropCatalog,
	dungeonID int64,
	characterLevel int,
	job byte,
	growType byte,
	rng *currentDungeonDropLCG,
	applyRewardRate bool,
) (int, uint32, string, bool, bool, error) {
	tier, matched := currentDungeonDimensionTier(catalog, dungeonID, characterLevel)
	if !matched {
		return 0, 0, "", false, false, nil
	}
	if rng == nil {
		return tier, 0, "", true, false, errCurrentDungeonDimensionDropInvalid
	}
	pool, found := catalog.ChronicleByCareer[currentDungeonDimensionChronicleKey{Job: job, GrowType: growType}]
	if !found || (len(pool.Normal) == 0 && len(pool.Set) == 0) {
		return tier, 0, "", true, false, nil
	}
	rate, found := catalog.ChronicleRates[tier]
	if !found {
		return tier, 0, "", true, false, fmt.Errorf("%w: dungeon_id=%d tier=%d has no chronicle rate", errCurrentDungeonDimensionDropInvalid, dungeonID, tier)
	}
	if applyRewardRate && (rate.RewardPercent == 0 || rng.Next(100) >= rate.RewardPercent) {
		return tier, 0, "", true, false, nil
	}
	selectedPool := pool.Normal
	group := "normal"
	if len(pool.Set) > 0 && rate.SetPercent > 0 && rng.Next(100) < rate.SetPercent {
		selectedPool = pool.Set
		group = "set"
	}
	if len(selectedPool) == 0 {
		selectedPool = pool.Set
		group = "set"
	}
	if len(selectedPool) == 0 {
		return tier, 0, "", true, false, nil
	}
	return tier, selectedPool[rng.Next(len(selectedPool))], group, true, true, nil
}

func currentDungeonDimensionSectionInts(document *dnfpvf.Document, section string, optional bool) ([]int64, error) {
	if document == nil {
		return nil, fmt.Errorf("%w: section=%s document=nil", errCurrentDungeonDimensionDropInvalid, section)
	}
	tokens, found := document.Section(section)
	if !found {
		if optional {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: %s section=[%s] missing", errCurrentDungeonDimensionDropInvalid, document.Path, section)
	}
	values := make([]int64, 0, len(tokens))
	for _, token := range tokens {
		if token.Kind == dnfpvf.TokenInt {
			values = append(values, token.Int)
		}
	}
	return values, nil
}

func currentDungeonDimensionDropDocumentMissing(err error) bool {
	return errors.Is(err, dnfpvf.ErrDocNotFound) || errors.Is(err, platformpvf.ErrFileNotFound)
}
