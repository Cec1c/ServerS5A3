package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

var (
	errDungeonDropObjectKeyRange     = errors.New("dnf dungeon drop object key range exhausted")
	errDungeonDropSceneSlotRange     = errors.New("dnf dungeon drop scene slot range exhausted")
	errDungeonDropObjectConflict     = errors.New("dnf dungeon drop object key already exists")
	errDungeonDropNotFound           = errors.New("dnf dungeon drop object was not found")
	errDungeonDropRoomMismatch       = errors.New("dnf dungeon drop belongs to another room")
	errDungeonDropActorMismatch      = errors.New("dnf dungeon drop belongs to another actor")
	errDungeonDropCoinBranchUnproved = errors.New("dnf dungeon coin/gold pickup branch is not proved")
)

type runtimeDungeonDropStatus byte

const (
	runtimeDungeonDropAvailable runtimeDungeonDropStatus = iota + 1
	runtimeDungeonDropConsumed
)

type runtimeDungeonDrop struct {
	ObjectKey           uint32
	SceneSlot           uint16
	Item                dungeonDropItemDefinition
	Amount              uint32
	QualitySeed         uint32
	DiscardOrigin       *runtimeDungeonDiscardOrigin
	OwnerActorObjectKey uint16
	Room                runtimeDungeonRoomKey
	Status              runtimeDungeonDropStatus
	DestinationSlot     uint16
	GoldAfter           int64
	// PartyShared 标记"这条掉落是**队伍副本实例共享**的"：同一实例内的任一成员都可以拾取，
	// `owned()` 因此不对它做 `OwnerActorObjectKey == 拾取者` 的相等校验。
	//
	// 由来（P0「组队深渊队员拿不到碎片」）：op38 掉落行里带的 `OwnerActorObjectKey`
	// 是**击杀者**的 actor key（`dungeon_hell_party_reward.go:644` 写入、`dungeon_death_response.go:91` 上线），
	// 而拾取校验要求它等于**拾取者**（`dungeon_drop_runtime.go` 的 `owned()`）⇒ 队员即使
	// 收到了那一帧、看得见地上的碎片，op43 也会被 `runtime_drop_owner_rejected` 拒掉。
	// 默认档（`DNFBRIDGE_ABYSS_PARTY_DROP_OWNER=party`）对队伍实例里的深渊奖励掉落置位；
	// `legacy` 档恒 false ⇒ 逐字节等于今天。单人（无实例）恒 false。
	PartyShared bool
	// EpicPieceBalance 是"这件掉落是史诗碎片、已入账号图鉴"的绝对值存档：
	// 只在拾取成功入账后写，用于**重放**同一次拾取时原样重发增量包
	// （与 GoldAfter / PickupResponseBody 同一条幂等路径，见 dungeon_drop_pickup.go）。
	EpicPieceBalance     uint32
	PickupResponseBody   []byte
	PickupItemUpdateBody []byte
	// 本局库存使用事务已将对应堆栈用完，不再要求结算时仍有背包行。
	ConsumptionReceipt *alignedcmd.StackableConsumption
	// MapOwnedPassiveObject 标记"这条掉落是 op29 预生成的建筑掉落"（厂商
	// `PassiveObjectDropEntry` / `PassiveObjectDropProjectionService.cs:122-140` 那条通道，
	// 见 dungeon_passive_object_drop.go）：它的身份是 **scene slot**（= START_MAP 里那条
	// extra entry 的 `GlobalSeq`），没有击杀者 actor，也不是客户端公告过的场景 objectKey。
	// 因此拾取查找允许按 scene slot 回退、并且不做"击杀者 actor 必须是自己"的校验
	// （厂商 `PassiveObjectDropEntry.ToDropInfo():14-25` 也不写 owner）。
	// 两个字段都只可能由 probe 档的投影写入 ⇒ 默认档下恒为零值，行为与今天逐字节一致。
	MapOwnedPassiveObject bool
	MapOwnedPassiveIndex  int
}

func (drop *runtimeDungeonDrop) isGold() bool {
	return drop != nil && drop.Item.ItemID == 0
}

type runtimeDungeonDropOwner struct {
	nextSceneSlot uint16
	byObjectKey   map[uint32]*runtimeDungeonDrop
	// bySceneSlot 是同一个账本的 scene slot 索引（与厂商 `run.Drops[SceneSlot]` 同键）。
	// 只登记 `MapOwnedPassiveObject` 的掉落，普通击杀掉落不进这张表 ⇒ 拾取的 slot
	// 回退路径永远找不到普通掉落。
	bySceneSlot map[uint16]*runtimeDungeonDrop
}

func newRuntimeDungeonDropOwner() *runtimeDungeonDropOwner {
	return &runtimeDungeonDropOwner{
		nextSceneSlot: 1,
		byObjectKey:   make(map[uint32]*runtimeDungeonDrop),
		bySceneSlot:   make(map[uint16]*runtimeDungeonDrop),
	}
}

func (owner *runtimeDungeonDropOwner) registerBatch(drops []*runtimeDungeonDrop, nextSceneSlot uint16) error {
	if owner == nil {
		return errDungeonDropNotFound
	}
	if owner.byObjectKey == nil {
		owner.byObjectKey = make(map[uint32]*runtimeDungeonDrop)
	}
	if owner.bySceneSlot == nil {
		owner.bySceneSlot = make(map[uint16]*runtimeDungeonDrop)
	}
	for _, drop := range drops {
		if drop == nil || drop.ObjectKey == 0 {
			return errDungeonDropNotFound
		}
		if _, exists := owner.byObjectKey[drop.ObjectKey]; exists {
			return fmt.Errorf("%w: object_key=%d", errDungeonDropObjectConflict, drop.ObjectKey)
		}
	}
	for _, drop := range drops {
		owner.byObjectKey[drop.ObjectKey] = drop
		if drop.MapOwnedPassiveObject {
			owner.bySceneSlot[drop.SceneSlot] = drop
		}
	}
	owner.nextSceneSlot = nextSceneSlot
	return nil
}

// mapOwnedPassiveObjectDrop 按 scene slot 取一条预生成建筑掉落（纯读）。
func (owner *runtimeDungeonDropOwner) mapOwnedPassiveObjectDrop(sceneSlot uint16) *runtimeDungeonDrop {
	if owner == nil {
		return nil
	}
	return owner.bySceneSlot[sceneSlot]
}

// mapOwnedPassiveObjectDropByRoom 按 (房间, 下标) 找已经登记过的同一条预生成掉落：
// 队伍共享实例下两个会话各自跑一次投影时，用它复用**同一个** scene slot（否则同一根
// 柱子会拿到两个 slot、两条行）。
func (owner *runtimeDungeonDropOwner) mapOwnedPassiveObjectDropByRoom(
	room runtimeDungeonRoomKey,
	objectIndex int,
) *runtimeDungeonDrop {
	if owner == nil {
		return nil
	}
	for _, drop := range owner.bySceneSlot {
		if drop == nil || !drop.MapOwnedPassiveObject {
			continue
		}
		if drop.MapOwnedPassiveIndex == objectIndex && drop.Room == room {
			return drop
		}
	}
	return nil
}

// ownedMapOwnedPassiveObjectBySceneSlot 是拾取路径的回退查找：客户端对一条 op29 预生成
// 掉落只可能知道它的 **scene slot**（= extra entry 的 `GlobalSeq`），不可能知道我们
// 发号器给的 objectKey。只接受 `MapOwnedPassiveObject` 的掉落，并且房间必须一致。
func (owner *runtimeDungeonDropOwner) ownedMapOwnedPassiveObjectBySceneSlot(
	sceneSlot uint16,
	room runtimeDungeonRoomKey,
	actorObjectKey ...uint16,
) (*runtimeDungeonDrop, error) {
	if owner == nil {
		return nil, errDungeonDropNotFound
	}
	drop := owner.bySceneSlot[sceneSlot]
	if drop == nil || !drop.MapOwnedPassiveObject {
		return nil, fmt.Errorf("%w: scene_slot=%d", errDungeonDropNotFound, sceneSlot)
	}
	if drop.Room != room {
		return nil, fmt.Errorf(
			"%w: scene_slot=%d owned=%+v current=%+v",
			errDungeonDropRoomMismatch,
			sceneSlot,
			drop.Room,
			room,
		)
	}
	if drop.OwnerActorObjectKey != 0 && (len(actorObjectKey) == 0 || actorObjectKey[0] != drop.OwnerActorObjectKey) {
		return nil, errDungeonDropActorMismatch
	}
	return drop, nil
}

func (owner *runtimeDungeonDropOwner) owned(
	objectKey uint32,
	room runtimeDungeonRoomKey,
	actorObjectKey uint16,
) (*runtimeDungeonDrop, error) {
	if owner == nil || objectKey == 0 {
		return nil, errDungeonDropNotFound
	}
	drop := owner.byObjectKey[objectKey]
	if drop == nil {
		return nil, fmt.Errorf("%w: object_key=%d", errDungeonDropNotFound, objectKey)
	}
	if drop.Room != room {
		return nil, fmt.Errorf(
			"%w: object_key=%d owned=%+v current=%+v",
			errDungeonDropRoomMismatch,
			objectKey,
			drop.Room,
			room,
		)
	}
	if drop.OwnerActorObjectKey != actorObjectKey && !drop.PartyShared {
		return nil, fmt.Errorf(
			"%w: object_key=%d owned=%d current=%d",
			errDungeonDropActorMismatch,
			objectKey,
			drop.OwnerActorObjectKey,
			actorObjectKey,
		)
	}
	return drop, nil
}

// currentDungeonDropAccountID 解析副本内**账号级**权益（成长契约/黑钻等）该读哪个
// 账号。
//
// 口径 = 会话账号（`runtimeDungeonState.AccountID`，建 runtime 时由
// accountIDForSession 冻结；见 dungeon_runtime_state.go 的字段注释）。依据：
//
//  1. 同族的两处账号级读取早就是这个口径 —— `dungeon_quest_drop.go` 与
//     `quest_finish.go` 都用 `s.accountIDForSession(session)` 调
//     `currentGrowthContractEffect`，只有掉落规划这一处曾经用角色行上的账号。
//  2. 这是**账号级 entitlement**（dnf_accounts 上的 premium 到期时间），不是角色
//     数据：它的归属口径必须与角色表、账号容器、账号级持久化一致（会话账号）。
//  3. 落回 `Character.AccountID` 会在"角色建在共享兜底账号 dnf:1、会话已由令牌
//     定案成 dnf:100xx"的现场形态下读到**别人的**契约状态（香港 2026-09-25 的
//     账号串号正是这个形态），进而给错掉落加成。
//
// 兼容档：runtime 没有带上会话账号时（历史路径 / 直接构造 runtime 的测试）退回
// `Character.AccountID`，与改动前逐字节同行为。
func currentDungeonDropAccountID(runtime *runtimeDungeonState) string {
	if runtime == nil {
		return ""
	}
	if accountID := strings.TrimSpace(runtime.AccountID); accountID != "" {
		return accountID
	}
	return strings.TrimSpace(runtime.Character.AccountID)
}

func (s *Service) planCurrentDungeonMonsterDrops(
	runtime *runtimeDungeonState,
	monster runtimeDungeonMonster,
	ownerActorObjectKey uint16,
) ([]currentDungeonDeathDropWire, error) {
	if runtime == nil || runtime.Session == nil || runtime.Room == nil {
		return nil, errDungeonWorldMapUnavailable
	}
	if ownerActorObjectKey == 0 {
		return nil, errDungeonDropActorMismatch
	}
	scene, ok := runtime.Session.Scene()
	if !ok {
		return nil, errDungeonWorldMapUnavailable
	}
	if currentTeamRaidGroundDropsDisabled(runtime) {
		return nil, nil
	}
	roomKey := runtimeDungeonRoomKeyFromScene(scene)
	monsterCatalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		return nil, err
	}
	rules, err := currentDungeonMonsterDropRules(monsterCatalog)
	if err != nil {
		return nil, err
	}
	dropCatalog, err := monsterCatalog.DropCatalog()
	if err != nil {
		return nil, err
	}
	pool, err := dropCatalog.MonsterPool(monster.Spawn.MonsterID)
	if err != nil {
		return nil, err
	}
	visit, err := runtime.currentDungeonRoomVisit(scene)
	if err != nil {
		return nil, err
	}
	monsterLevel, err := currentDungeonMonsterLevel(monster.Spawn, runtime.Dungeon.Metadata.BasisLevel)
	if err != nil {
		return nil, err
	}
	monsterType, err := currentDungeonMonsterType(monster.Spawn.Rank)
	if err != nil {
		return nil, err
	}
	rng := newCurrentDungeonDropLCG(visit.DropRNG)
	probability, probabilityFound := rules.Probability(int64(monsterLevel))
	itemReference, itemReferenceFound := rules.ItemReference(int64(monsterLevel))
	difficultyRate := currentDungeonDropDifficultyRate(int(runtime.Request.Difficulty))
	raw := rules.RawTables()

	type plannedDrop struct {
		itemID uint32
		amount uint32
		gold   bool
	}
	selectedDrops := make([]plannedDrop, 0, 8)

	// 86JP computes gold before the ordinary item-rate rolls, and gold is a
	// normal scene drop with template id 0. Older Go intentionally consumed the
	// roll but threw the object away because the coin pickup branch was still
	// being isolated; that made live dungeons never show金币.
	if probabilityFound {
		goldReferences, goldErr := currentDungeonGoldReferences(monsterCatalog)
		if goldErr != nil {
			return nil, goldErr
		}
		goldReference, goldFound := goldReferences[int64(monsterLevel)]
		if !goldFound {
			return nil, fmt.Errorf("%w: monster_level=%d", errCurrentDungeonGoldReferenceInvalid, monsterLevel)
		}
		goldAmount := currentDungeonGoldAmount(rng, goldReference, difficultyRate)
		goldRate := currentDungeonDropRate(probability.Rates[0], raw.MonsterTypeBonus[0][monsterType], difficultyRate)
		goldRoll := rng.Next(currentDungeonDropDenominator)
		goldSelected := goldRate > goldRoll
		s.logGameEvent(nil, "game-dungeon-monster-drop-gold-roll",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"monster_id", monster.Spawn.MonsterID,
			"monster_object_key", monster.ObjectKey,
			"monster_level", monsterLevel,
			"monster_type", monsterType,
			"base_rate", probability.Rates[0],
			"monster_type_bonus", raw.MonsterTypeBonus[0][monsterType],
			"difficulty_bonus", difficultyRate,
			"gold_rate", goldRate,
			"gold_roll", goldRoll,
			"gold_amount", goldAmount,
			"selected", goldSelected,
			"formula_source", "86jp_monster_gold_branch_itemdropinfo_common_and_monseter")
		if goldSelected {
			selectedDrops = append(selectedDrops, plannedDrop{amount: goldAmount, gold: true})
		}
	}

	dimensionImageMaterials := false
	appendSelected := func(itemID uint32) error {
		// The image contract owns these two quantities once per special kill.
		// Generic/independent tables must not add another one-item stack.
		if dimensionImageMaterials && (itemID == 3311 || itemID == 10303569) {
			return nil
		}
		if itemID == 0 {
			// Real gold is selected only through the gold-rate branch above,
			// where the amount comes from ItemDropInfo_Common.etc.
			return nil
		}
		if _, err := dropCatalog.ResolveItem(itemID); err != nil {
			return err
		}
		selectedDrops = append(selectedDrops, plannedDrop{itemID: itemID, amount: 1})
		return nil
	}
	dimensionDrops, err := currentDungeonDimensionDrops(monsterCatalog)
	if err != nil {
		return nil, err
	}
	dimensionDrops, err = currentRuntimeDimensionDrops(dimensionDrops, runtime)
	if err != nil {
		return nil, err
	}
	dimensionTier, dimensionPool, dimensionMatched, err := currentDungeonDimensionDropPool(
		dimensionDrops,
		runtime.Dungeon.ID,
		runtime.Character.Level,
	)
	if err != nil {
		return nil, err
	}
	// 当前异界地图的两种次数各自控制随机装备与指定材料；其它副本沿用既有路径。
	randomDropAttempts, fixedDropAttempts := 1, 1
	if dimensionMatched {
		randomDropAttempts, err = currentDungeonMapDropAttempts(monster.Spawn.RandomDropCount, "random")
		if err != nil {
			return nil, err
		}
		fixedDropAttempts, err = currentDungeonMapDropAttempts(monster.Spawn.FixedDropCount, "specified")
		if err != nil {
			return nil, err
		}
	}
	dimensionRank := currentDimensionRewardRank(runtime, monster, monsterType)
	dimensionImageSpecial := dimensionMatched && runtime.Dungeon.Metadata.Flags["dimension dungeon"] && dimensionRank != 0 && fixedDropAttempts > 0
	dimensionImageMaterials = dimensionImageSpecial
	characterJob := byte(0)
	characterGrowType := byte(runtime.Character.Stats["grow_type"]) & 0x0f
	if dimensionMatched && len(dimensionDrops.ChronicleByCareer) > 0 {
		jobValue, parseErr := strconv.ParseUint(strings.TrimSpace(runtime.Character.Job), 10, 8)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: dimension character job=%q", errCurrentDungeonDimensionDropInvalid, runtime.Character.Job)
		}
		characterJob = byte(jobValue)
	}
	selectedDimensionChronicleItems := make([]uint32, 0, randomDropAttempts)
	selectedDimensionChronicleGroups := make([]string, 0, randomDropAttempts)
	// The image specifies one career item for every elite/boss. The PVF
	// normal/set pool split remains authoritative; no outer equipment roll.
	if dimensionImageSpecial && len(dimensionDrops.ChronicleByCareer) > 0 {
		_, itemID, group, _, selected, err := currentDungeonDimensionChronicleItem(dimensionDrops, runtime.Dungeon.ID, runtime.Character.Level, characterJob, characterGrowType, rng, false)
		if err != nil {
			return nil, err
		}
		if selected {
			if err := appendSelected(itemID); err != nil {
				return nil, err
			}
			selectedDimensionChronicleItems = append(selectedDimensionChronicleItems, itemID)
			selectedDimensionChronicleGroups = append(selectedDimensionChronicleGroups, group)
		}
	}
	if monster.Definition.DimensionHiddenBoss {
		if !runtime.dimensionHiddenHighReward {
			return nil, fmt.Errorf("%w: hidden boss death without committed weekly reward admission", errCurrentDungeonDimensionDropInvalid)
		}
		if !dimensionMatched || runtime.MazeIndex < 0 || runtime.MazeIndex >= len(runtime.Dungeon.Mazes) || !runtime.Dungeon.Mazes[runtime.MazeIndex].HiddenBoss {
			return nil, fmt.Errorf("%w: hidden boss outside dimension hidden maze", errCurrentDungeonDimensionDropInvalid)
		}
		hiddenItems, hiddenErr := currentDimensionHiddenDropItems(dimensionDrops.HiddenBossByTier[dimensionTier], rng)
		if hiddenErr != nil {
			return nil, hiddenErr
		}
		for _, itemID := range hiddenItems {
			if err := appendSelected(itemID); err != nil {
				return nil, err
			}
		}
		s.logGameEvent(nil, "game-dungeon-dimension-hidden-boss-drop-roll",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"monster_id", monster.Spawn.MonsterID,
			"dimension_tier", dimensionTier,
			"candidate_count", len(dimensionDrops.HiddenBossByTier[dimensionTier]),
			"selected_items", hiddenItems,
			"source", currentDungeonDimensionInfoPath+"/[hidden boss drop table]")
	}
	if probabilityFound && itemReferenceFound {
		for attempt := 0; attempt < randomDropAttempts; attempt++ {
			if rate := currentDungeonDropRate(probability.Rates[1], raw.MonsterTypeBonus[1][monsterType], difficultyRate); rate > rng.Next(currentDungeonDropDenominator) {
				rarity := currentDungeonDropRarity(rng, rules)
				itemID, selected, err := dropCatalog.SelectGenericStackable(rng, int(monsterLevel), itemReference.ValueA, itemReference.ValueB, rarity)
				if err != nil {
					return nil, err
				}
				if selected {
					if err := appendSelected(itemID); err != nil {
						return nil, err
					}
				}
			}
			if rate := currentDungeonDropRate(probability.Rates[2], raw.MonsterTypeBonus[2][monsterType], difficultyRate); !dimensionImageSpecial && rate > rng.Next(currentDungeonDropDenominator) {
				var itemID uint32
				var selected bool
				if dimensionMatched && len(dimensionDrops.ChronicleByCareer) > 0 {
					var group string
					_, itemID, group, _, selected, err = currentDungeonDimensionChronicleItem(
						dimensionDrops,
						runtime.Dungeon.ID,
						runtime.Character.Level,
						characterJob,
						characterGrowType,
						rng,
						false,
					)
					if err != nil {
						return nil, err
					}
					if selected {
						selectedDimensionChronicleItems = append(selectedDimensionChronicleItems, itemID)
						selectedDimensionChronicleGroups = append(selectedDimensionChronicleGroups, group)
					}
				} else {
					rarity := currentDungeonDropRarityWithMarble(rng, rules, runtime.attendanceMarbleBuffs&1 != 0)
					itemID, selected, err = dropCatalog.selectOrdinaryMonsterEquipment(rng, int(monsterLevel), itemReference.ValueA, itemReference.ValueB, rarity)
					if err != nil {
						return nil, err
					}
				}
				if selected {
					if err := appendSelected(itemID); err != nil {
						return nil, err
					}
				}
			}
			if rate := currentDungeonDropRate(probability.Rates[3], raw.MonsterTypeBonus[3][monsterType], difficultyRate); rate > rng.Next(currentDungeonDropDenominator) {
				rarity := currentDungeonDropRarity(rng, rules)
				itemID, selected, err := dropCatalog.SelectGenericStackable(rng, int(monsterLevel), itemReference.ValueA, itemReference.ValueB, rarity)
				if err != nil {
					return nil, err
				}
				if selected {
					if err := appendSelected(itemID); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	dimensionCandidates := make([]uint32, 0, len(dimensionPool))
	for _, entry := range dimensionPool {
		dimensionCandidates = append(dimensionCandidates, entry.ItemID)
	}
	selectedSpecialDimensionItems := make([]uint32, 0, fixedDropAttempts*len(dimensionPool))
	if dimensionRank == 0 {
		// Normal monsters expose dimension materials only as ordinary area
		// candidates. They therefore retain the shared 1000/10000 outer roll
		// and cannot inherit the fixed special-monster projection.
		pool = append(pool, dimensionPool...)
	} else {
		// Champion, super-champion and boss actors keep the dimension dungeon's
		// special fixed projection. This was the original fragment path before
		// normal monsters were separated from it; keep the PVF fixed_drop_count
		// and every tier/group row authoritative rather than inventing another
		// rank probability.
		for attempt := 0; attempt < fixedDropAttempts; attempt++ {
			for _, entry := range dimensionPool {
				if amount, specified := currentDimensionMaterialAmount(dimensionTier, dimensionRank, entry.ItemID); specified {
					if attempt == 0 && amount > 0 {
						if _, err := dropCatalog.ResolveItem(entry.ItemID); err != nil {
							return nil, err
						}
						selectedDrops = append(selectedDrops, plannedDrop{itemID: entry.ItemID, amount: amount})
						selectedSpecialDimensionItems = append(selectedSpecialDimensionItems, entry.ItemID)
					}
					continue
				}
				if err := appendSelected(entry.ItemID); err != nil {
					return nil, err
				}
				selectedSpecialDimensionItems = append(selectedSpecialDimensionItems, entry.ItemID)
			}
		}
	}
	selectedPoolItems := make([]uint32, 0, fixedDropAttempts)
	if len(pool) > 0 {
		for attempt := 0; attempt < fixedDropAttempts; attempt++ {
			if currentDungeonExplicitPoolRate <= rng.Next(currentDungeonDropDenominator) {
				continue
			}
			poolEntry, selected := selectDungeonMonsterDrop(pool, uint64(rng.NextUint32()))
			if selected {
				if err := appendSelected(poolEntry.ItemID); err != nil {
					return nil, err
				}
				selectedPoolItems = append(selectedPoolItems, poolEntry.ItemID)
			}
		}
	}
	if dimensionMatched {
		eventName := "game-dungeon-monster-dimension-ordinary-pool-roll"
		dropMode := "ordinary_pool"
		if dimensionRank != 0 {
			eventName = "game-dungeon-monster-dimension-special-rank-projection"
			dropMode = "special_rank_fixed"
		}
		s.logGameEvent(nil, eventName,
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"monster_id", monster.Spawn.MonsterID,
			"monster_object_key", monster.ObjectKey,
			"monster_rank", monster.Spawn.Rank,
			"monster_type", monsterType,
			"character_level", runtime.Character.Level,
			"dimension_tier", dimensionTier,
			"dimension_reward_rank", dimensionRank,
			"image_special_reward", dimensionImageSpecial,
			"drop_mode", dropMode,
			"ordinary_pool_rate", currentDungeonExplicitPoolRate,
			"ordinary_pool_denominator", currentDungeonDropDenominator,
			"ordinary_pool_attempts", fixedDropAttempts,
			"ordinary_pool_candidate_count", len(pool),
			"dimension_candidate_items", dimensionCandidates,
			"selected_pool_items", selectedPoolItems,
			"selected_special_dimension_items", selectedSpecialDimensionItems,
			"selected_chronicle_items", selectedDimensionChronicleItems,
			"selected_chronicle_groups", selectedDimensionChronicleGroups,
			"chronicle_job", characterJob,
			"chronicle_grow_type", characterGrowType,
			"special_probability_shared", false,
			"source", currentDungeonDimensionGatePath+"+"+currentDungeonDimensionInfoPath+"+"+currentDungeonDimensionDropListPath+"+"+currentDungeonDimensionDropExcludePath+"+rank_split_monster_template_pool")
	}
	specialDrops, err := currentDungeonSpecialDrops(monsterCatalog)
	if err != nil {
		return nil, err
	}
	dungeonLevel := int(monsterLevel)
	if basisLevel := runtime.Dungeon.Metadata.BasisLevel; basisLevel.Set && basisLevel.Value > 0 {
		dungeonLevel = int(basisLevel.Value)
	}
	growthIndependentDropPercent := int64(0)
	if growthEffect, growthActive := s.currentGrowthContractEffect(
		context.Background(),
		currentDungeonDropAccountID(runtime),
	); growthActive {
		// This runtime currently admits one locally owned dungeon actor. The
		// PVF's first independent-drop value is therefore authoritative.
		growthIndependentDropPercent = growthEffect.independentDropRatePercent(1)
	}
	independentItems := currentDungeonIndependentDropItems(
		specialDrops,
		monster.Spawn.MonsterID,
		int(runtime.Request.Difficulty),
		dungeonLevel,
		growthIndependentDropPercent,
		rng,
	)
	for _, itemID := range independentItems {
		if err := appendSelected(itemID); err != nil {
			return nil, err
		}
	}
	worldLevel := specialDrops.WorldByLevel[int(monsterLevel)]
	worldItemID, worldSelected := currentDungeonWorldDropItem(specialDrops, int(monsterLevel), rng)
	if worldSelected {
		if err := appendSelected(worldItemID); err != nil {
			return nil, err
		}
	}
	carbonCrystalSelected := currentDungeonOrdinaryMonsterLooksBoss(monster) && currentDungeonCarbonCrystalEligible(runtime, dropCatalog)
	if carbonCrystalSelected {
		if err := appendSelected(currentDungeonCarbonCrystalItemID); err != nil {
			return nil, err
		}
	}
	s.logGameEvent(nil, "game-dungeon-monster-special-drop-roll",
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"monster_id", monster.Spawn.MonsterID,
		"monster_level", monsterLevel,
		"dungeon_basis_level", dungeonLevel,
		"difficulty", runtime.Request.Difficulty,
		"independent_entry_count", len(specialDrops.IndependentByMonster[monster.Spawn.MonsterID]),
		"independent_selected_items", independentItems,
		"growth_contract_independent_drop_percent", growthIndependentDropPercent,
		"world_total_weight", worldLevel.TotalWeight,
		"world_candidate_count", len(worldLevel.Items),
		"world_denominator", currentDungeonWorldDropDenominator,
		"world_selected", worldSelected,
		"world_selected_item", worldItemID,
		"carbon_crystal_guaranteed", carbonCrystalSelected,
		"source", "runtime_pvf_independent_drop_and_world_drop")

	// The LCG is advanced even when no standard scene item survived the real
	// PVF selection.  This mirrors the reference room stream across kills.
	visit.DropRNG = rng.Seed()
	if runtime.raidEntry != nil && runtime.raidEntry.ChannelType == 32 {
		filtered := selectedDrops[:0]
		for _, selected := range selectedDrops {
			if !selected.gold {
				definition, err := dropCatalog.ResolveItem(selected.itemID)
				if err != nil {
					return nil, err
				}
				if s.currentLukeTeamMonsterMaterialSuppressed(runtime, definition) {
					continue
				}
			}
			filtered = append(filtered, selected)
		}
		selectedDrops = filtered
	}
	if len(selectedDrops) == 0 {
		return nil, nil
	}
	owner := runtime.DropOwner
	if owner == nil {
		owner = newRuntimeDungeonDropOwner()
	}
	count := uint32(len(selectedDrops))
	// Allocate keys and register the batch under the same instance lock so
	// simultaneous kills cannot reserve overlapping ground objects.
	instance := runtime.lockPartyDungeonInstance()
	defer unlockPartyDungeonInstance(instance)
	if abyssPartyDropOwnerModeFromEnv().sharesLedger() {
		if shared := partySharedDungeonDropOwnerLocked(runtime); shared != nil {
			owner = shared
		}
	}
	nextKeyStart := runtime.NextObjectKey
	if instance != nil {
		nextKeyStart = instance.nextObjectKey
	}
	if nextKeyStart == 0 || nextKeyStart > math.MaxUint16 ||
		count > math.MaxUint16-nextKeyStart {
		return nil, fmt.Errorf(
			"%w: next=%d count=%d",
			errDungeonDropObjectKeyRange,
			nextKeyStart,
			count,
		)
	}
	// 共享实例下掉落账本是**两个会话共用**的（各自只持有自己的 session.dungeon.mu），
	// 所以读改写统一套一层实例锁；单人路径 lockPartyDungeonInstance 返回 nil，无副作用。
	if owner.nextSceneSlot == 0 || uint32(owner.nextSceneSlot)+count > math.MaxUint16 {
		return nil, fmt.Errorf(
			"%w: next=%d count=%d",
			errDungeonDropSceneSlotRange,
			owner.nextSceneSlot,
			count,
		)
	}

	drops := make([]*runtimeDungeonDrop, 0, count)
	wires := make([]currentDungeonDeathDropWire, 0, count)
	nextObjectKey := nextKeyStart
	nextSceneSlot := owner.nextSceneSlot
	for _, selected := range selectedDrops {
		definition := dungeonDropItemDefinition{ItemID: 0, Kind: dungeonDropItemStackable}
		if !selected.gold {
			var err error
			definition, err = dropCatalog.ResolveItem(selected.itemID)
			if err != nil {
				return nil, err
			}
		}
		itemState, err := currentDungeonDeathDropItemState(nextSceneSlot, definition, selected.amount)
		if err != nil {
			return nil, err
		}
		freezeCurrentAikenInitialDrop(&definition, itemState)
		qualitySeed := uint32(0)
		if definition.Kind == dungeonDropItemEquipment {
			qualitySeed = binary.LittleEndian.Uint32(itemState.data[6:10])
		}
		drop := &runtimeDungeonDrop{
			ObjectKey:           nextObjectKey,
			SceneSlot:           nextSceneSlot,
			Item:                definition,
			Amount:              selected.amount,
			QualitySeed:         qualitySeed,
			OwnerActorObjectKey: ownerActorObjectKey,
			Room:                roomKey,
			Status:              runtimeDungeonDropAvailable,
		}
		drops = append(drops, drop)
		wires = append(wires, currentDungeonDeathDropWire{
			SceneObjectKey:      drop.ObjectKey,
			Item:                itemState,
			UnknownTailSentinel: math.MaxUint16,
			OwnerActorObjectKey: ownerActorObjectKey,
		})
		nextObjectKey++
		nextSceneSlot++
	}
	if err := owner.registerBatch(drops, nextSceneSlot); err != nil {
		return nil, err
	}
	runtime.DropOwner = owner
	runtime.commitDungeonNextObjectKeyLocked(nextObjectKey)
	return wires, nil
}
