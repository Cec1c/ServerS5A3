package inventory

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"time"

	dnfplacement "longheng.io/server/internal/modules/dnf/itemplacement"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 合成升品质概率采用当前约定表；升级概率与合成产出权重读取PVF。
// 合成始终产一张卡，概率只决定是否提升一个品质。
func MonsterCardCompoundPercent(rarity, count int) (int, bool) {
	rates := [4][6]int{
		{0, 0, 20, 40, 60, 80},
		{0, 0, 10, 25, 40, 0},
		{0, 0, 5, 15, 0, 0},
		{0, 0, 0, 0, 0, 0},
	}
	limits := [4]int{5, 4, 3, 2}
	if rarity < 0 || rarity >= len(rates) || count < 2 || count > limits[rarity] {
		return 0, false
	}
	return rates[rarity][count], true
}

type MonsterCardDefinition struct {
	ItemID           int64
	PVFPath          string
	StackableType    string
	Rarity           int
	IsCard, IsBinder bool
	Stages           []byte
	StackLimit       int64
	ExpireAt         time.Time
	Policy           dnfplacement.SlotPolicy
}

type MonsterCardResolver func(int64) (MonsterCardDefinition, error)

type MonsterCardWeightedReward struct {
	ItemID int64
	Weight int
}

type MonsterCardRules struct {
	UpgradePercent map[int]int
	MaximumInputs  map[int]int
	Pools          map[int][]MonsterCardWeightedReward
}

type MonsterCardCommand struct {
	CharacterID                          uint16
	ListType                             byte
	TargetSlot, MaterialSlot, BinderSlot int16
	MaterialCount                        uint16
	InputSlots                           []int16
	// 测试可注入确定性随机；客户端请求不能提供此字段。
	Random func(int) int
}

type MonsterCardResult struct {
	Applied, Upgraded bool
	ErrorCode         byte
	Reason            string
	ItemID            int64
	Level             byte
	ChancePercent     int
	ResultSlot        int16
	ChangedSlots      []int16
	Inventory         dnfrepo.InventoryRecord
}

func DecodeMonsterCardUpgrade(body []byte) (MonsterCardCommand, error) {
	if len(body) != 7 {
		return MonsterCardCommand{}, fmt.Errorf("卡片升级包长%d，预期7", len(body))
	}
	return MonsterCardCommand{ListType: body[0], TargetSlot: int16(binary.LittleEndian.Uint16(body[1:3])), MaterialCount: binary.LittleEndian.Uint16(body[3:5]), MaterialSlot: int16(binary.LittleEndian.Uint16(body[5:7]))}, nil
}

func DecodeMonsterCardCompound(body []byte) (MonsterCardCommand, error) {
	if len(body) < 3 {
		return MonsterCardCommand{}, fmt.Errorf("卡片合成包过短")
	}
	// 当前414包无list字节：合成器槽:u16, 张数:u8, 张数个卡片槽:u16。
	count := int(body[2])
	if count < 2 || count > 5 || len(body) != 3+count*2 {
		return MonsterCardCommand{}, fmt.Errorf("卡片合成张数或包长无效")
	}
	cmd := MonsterCardCommand{ListType: 0, BinderSlot: int16(binary.LittleEndian.Uint16(body[0:2]))}
	for i := 0; i < count; i++ {
		cmd.InputSlots = append(cmd.InputSlots, int16(binary.LittleEndian.Uint16(body[3+i*2:5+i*2])))
	}
	return cmd, nil
}

func cardRejected(reason string) MonsterCardResult {
	return MonsterCardResult{ErrorCode: 0x13, Reason: reason, ResultSlot: -1}
}

func cardRandom(cmd MonsterCardCommand, n int) (int, error) {
	if n <= 0 {
		return 0, fmt.Errorf("卡片随机范围无效")
	}
	fn := cmd.Random
	if fn == nil {
		fn = rand.Intn
	}
	value := fn(n)
	if value < 0 || value >= n {
		return 0, fmt.Errorf("卡片随机结果越界")
	}
	return value, nil
}

func cardStageValid(stages []byte, level byte) bool {
	for _, stage := range stages {
		if stage == level {
			return true
		}
	}
	return false
}
func cardNextStage(stages []byte, level byte) (byte, bool) {
	var next byte
	found := false
	for _, stage := range stages {
		if stage > level && (!found || stage < next) {
			next, found = stage, true
		}
	}
	return next, found
}

func (o *Owner) UpgradeMonsterCard(ctx context.Context, cmd MonsterCardCommand, resolve MonsterCardResolver, rules MonsterCardRules) (MonsterCardResult, error) {
	if o == nil || o.repo == nil || cmd.CharacterID == 0 || resolve == nil {
		return cardRejected("缺少角色、库存或实际PVF目录"), nil
	}
	if !o.inItemTransaction {
		var result MonsterCardResult
		err := o.withinInventoryTransaction(ctx, strconv.Itoa(int(cmd.CharacterID)), func(tx *Owner) error {
			var e error
			result, e = tx.UpgradeMonsterCard(ctx, cmd, resolve, rules)
			return e
		})
		return result, err
	}
	if cmd.ListType != 0 || cmd.MaterialCount != 1 || cmd.TargetSlot < 0 || cmd.MaterialSlot < 0 {
		return cardRejected("升级只接受主背包的一张材料卡"), nil
	}
	record, state, found, err := o.loadMonsterCardInventory(ctx, strconv.Itoa(int(cmd.CharacterID)))
	if err != nil {
		return MonsterCardResult{}, err
	}
	if !found {
		return cardRejected("角色库存不存在"), nil
	}
	record = dnfrepo.CloneInventory(record)
	target, targetOK := record.Slots[slotKey(0, cmd.TargetSlot)]
	material, materialOK := record.Slots[slotKey(0, cmd.MaterialSlot)]
	if !targetOK || !materialOK || target.Count < 1 || material.Count < 1 || (cmd.TargetSlot == cmd.MaterialSlot && target.Count < 2) {
		return cardRejected("目标或材料数量不足"), nil
	}
	td, err := resolve(target.ItemID)
	if err != nil {
		return MonsterCardResult{}, err
	}
	md, err := resolve(material.ItemID)
	if err != nil {
		return MonsterCardResult{}, err
	}
	if !cardSourceValid(target, td, cmd.TargetSlot, state) || !cardSourceValid(material, md, cmd.MaterialSlot, state) || td.Rarity != md.Rarity {
		return cardRejected("升级目标与材料必须是同品质怪物卡片"), nil
	}
	level, valid := dnfrepo.MonsterCardUpgradeCount(target)
	next, hasNext := cardNextStage(td.Stages, level)
	if !valid || !cardStageValid(td.Stages, level) || !hasNext {
		return cardRejected("目标已达升级上限或升级状态无效"), nil
	}
	materialLevel, valid := dnfrepo.MonsterCardUpgradeCount(material)
	if !valid || !cardStageValid(md.Stages, materialLevel) {
		return cardRejected("材料卡片升级状态无效"), nil
	}
	percent, known := rules.UpgradePercent[td.Rarity]
	if !known || percent < 0 || percent > 100 {
		return cardRejected("该品质未配置合法升级概率"), nil
	}
	// 先演算成功分格。任何成功路径放不下时不抽奖、不扣材料。
	plan := dnfrepo.CloneInventory(record)
	if _, _, err = applyCardUpgrade(&plan, cmd, td, next, true, state); err != nil {
		return MonsterCardResult{ErrorCode: 4, Reason: err.Error(), ResultSlot: -1}, nil
	}
	roll := 0
	if percent < 100 {
		roll, err = cardRandom(cmd, 100)
		if err != nil {
			return MonsterCardResult{}, err
		}
	}
	success := percent == 100 || roll < percent
	resultSlot, changed, err := applyCardUpgrade(&record, cmd, td, next, success, state)
	if err != nil {
		return MonsterCardResult{}, err
	}
	if !success {
		next = level
	}
	record.UpdatedAt = time.Now()
	if err = dnfrepo.SaveInventoryFields(ctx, o.repo, record, dnfrepo.InventoryFieldSlots); err != nil {
		return MonsterCardResult{}, err
	}
	return MonsterCardResult{Applied: true, Upgraded: success, ItemID: target.ItemID, Level: next, ChancePercent: percent, ResultSlot: resultSlot, ChangedSlots: cardChangedSlots(changed), Inventory: record}, nil
}

func cardSourceValid(stack dnfrepo.ItemStack, def MonsterCardDefinition, slot int16, state *dnfrepo.CharacterContainerState) bool {
	return def.IsCard && def.ItemID == stack.ItemID && def.PVFPath != "" && stack.Count > 0 && !isEquipmentLocked(stack) && (stack.ExpireAt.IsZero() || stack.ExpireAt.After(time.Now())) && newCardPlacementPolicy(def.Policy, state).Contains(0, slot)
}

func consumeCard(items map[string]dnfrepo.ItemStack, slot int16, count int64) error {
	key := slotKey(0, slot)
	stack, ok := items[key]
	if count <= 0 || !ok || stack.Count < count {
		return fmt.Errorf("卡片材料数量不足，槽%d", slot)
	}
	stack = cloneStack(stack)
	stack.Count -= count
	if stack.Count == 0 {
		delete(items, key)
	} else {
		updateStackRawAmount(&stack)
		items[key] = stack
	}
	return nil
}

func applyCardUpgrade(record *dnfrepo.InventoryRecord, cmd MonsterCardCommand, def MonsterCardDefinition, next byte, success bool, state *dnfrepo.CharacterContainerState) (int16, map[int16]bool, error) {
	target := cloneStack(record.Slots[slotKey(0, cmd.TargetSlot)])
	changed := map[int16]bool{cmd.MaterialSlot: true}
	if err := consumeCard(record.Slots, cmd.MaterialSlot, 1); err != nil {
		return -1, nil, err
	}
	if !success {
		return cmd.TargetSlot, changed, nil
	}
	if err := consumeCard(record.Slots, cmd.TargetSlot, 1); err != nil {
		return -1, nil, err
	}
	changed[cmd.TargetSlot] = true
	target.Count = 1
	dnfrepo.SetMonsterCardUpgradeCount(&target, next)
	target.Extra["stack_limit"] = strconv.FormatInt(def.StackLimit, 10)
	if def.StackableType != "" {
		target.Extra["stackable_type"] = def.StackableType
	}
	updateStackRawAmount(&target)
	slot, err := placeCard(record, target, def, cmd.TargetSlot, state)
	if err != nil {
		return -1, nil, err
	}
	changed[slot] = true
	return slot, changed, nil
}

func placeCard(record *dnfrepo.InventoryRecord, incoming dnfrepo.ItemStack, def MonsterCardDefinition, preferred int16, state *dnfrepo.CharacterContainerState) (int16, error) {
	policy := newCardPlacementPolicy(def.Policy, state)
	limit := def.StackLimit
	if limit == 0 {
		limit = math.MaxInt32
	}
	if limit < 1 {
		return -1, fmt.Errorf("卡片堆叠上限无效")
	}
	for _, slot := range policy.MergeSlots() {
		if slot == preferred {
			continue
		}
		key := slotKey(0, slot)
		old, ok := record.Slots[key]
		if !ok || old.Count < 1 || old.Count >= limit || !dnfrepo.CanMergeItemStack(old, incoming) {
			continue
		}
		old = cloneStack(old)
		old.Count++
		updateStackRawAmount(&old)
		record.Slots[key] = old
		return slot, nil
	}
	if policy.Contains(0, preferred) {
		if _, occupied := record.Slots[slotKey(0, preferred)]; !occupied {
			record.Slots[slotKey(0, preferred)] = incoming
			return preferred, nil
		}
	}
	for _, slot := range policy.EmptySlots() {
		if _, occupied := record.Slots[slotKey(0, slot)]; !occupied {
			record.Slots[slotKey(0, slot)] = incoming
			return slot, nil
		}
	}
	return -1, fmt.Errorf("升级或合成结果没有合法空格")
}

func (o *Owner) CompoundMonsterCards(ctx context.Context, cmd MonsterCardCommand, resolve MonsterCardResolver, rules MonsterCardRules) (MonsterCardResult, error) {
	if o == nil || o.repo == nil || cmd.CharacterID == 0 || resolve == nil {
		return cardRejected("缺少角色、库存或实际PVF目录"), nil
	}
	if !o.inItemTransaction {
		var result MonsterCardResult
		err := o.withinInventoryTransaction(ctx, strconv.Itoa(int(cmd.CharacterID)), func(tx *Owner) error {
			var e error
			result, e = tx.CompoundMonsterCards(ctx, cmd, resolve, rules)
			return e
		})
		return result, err
	}
	if cmd.ListType != 0 || len(cmd.InputSlots) < 2 || len(cmd.InputSlots) > 5 || cmd.BinderSlot < 0 {
		return cardRejected("合成列表、数量或合成器槽无效"), nil
	}
	record, state, found, err := o.loadMonsterCardInventory(ctx, strconv.Itoa(int(cmd.CharacterID)))
	if err != nil {
		return MonsterCardResult{}, err
	}
	if !found {
		return cardRejected("角色库存不存在"), nil
	}
	record = dnfrepo.CloneInventory(record)
	binder, found := record.Slots[slotKey(0, cmd.BinderSlot)]
	if !found || binder.Count < 1 || isEquipmentLocked(binder) {
		return cardRejected("合成器不存在或已锁定"), nil
	}
	bd, err := resolve(binder.ItemID)
	if err != nil {
		return MonsterCardResult{}, err
	}
	if !bd.IsBinder || bd.ItemID != binder.ItemID || !newCardPlacementPolicy(bd.Policy, state).Contains(0, cmd.BinderSlot) || (!binder.ExpireAt.IsZero() && !binder.ExpireAt.After(time.Now())) {
		return cardRejected("该物品不是合法卡片合成器"), nil
	}
	counts := map[int16]int64{}
	rarity := -1
	for _, slot := range cmd.InputSlots {
		if slot < 0 || slot == cmd.BinderSlot {
			return cardRejected("合成材料槽无效"), nil
		}
		counts[slot]++
		stack, ok := record.Slots[slotKey(0, slot)]
		if !ok || stack.Count < counts[slot] {
			return cardRejected("合成材料数量不足"), nil
		}
		def, e := resolve(stack.ItemID)
		if e != nil {
			return MonsterCardResult{}, e
		}
		level, valid := dnfrepo.MonsterCardUpgradeCount(stack)
		if !cardSourceValid(stack, def, slot, state) || !valid || !cardStageValid(def.Stages, level) {
			return cardRejected("合成材料不是合法怪物卡片"), nil
		}
		if rarity < 0 {
			rarity = def.Rarity
		} else if rarity != def.Rarity {
			return cardRejected("合成只能使用同品质卡片"), nil
		}
	}
	percent, valid := MonsterCardCompoundPercent(rarity, len(cmd.InputSlots))
	if !valid || len(cmd.InputSlots) > rules.MaximumInputs[rarity] {
		return cardRejected("合成张数超过该品质PVF上限"), nil
	}
	roll := 0
	if percent > 0 {
		roll, err = cardRandom(cmd, 100)
		if err != nil {
			return MonsterCardResult{}, err
		}
	}
	upgraded := percent > 0 && roll < percent
	resultRarity := rarity
	if upgraded {
		resultRarity++
	}
	pool := rules.Pools[resultRarity]
	total := int64(0)
	for _, entry := range pool {
		if entry.ItemID <= 0 || entry.Weight < 0 {
			return MonsterCardResult{}, fmt.Errorf("PVF卡片产出池无效")
		}
		if int64(entry.Weight) > math.MaxInt32-total {
			return MonsterCardResult{}, fmt.Errorf("PVF卡片产出池权重溢出")
		}
		total += int64(entry.Weight)
	}
	if total <= 0 || total > math.MaxInt32 {
		return MonsterCardResult{}, fmt.Errorf("PVF卡片产出池为空或权重溢出")
	}
	choice, e := cardRandom(cmd, int(total))
	if e != nil {
		return MonsterCardResult{}, e
	}
	var rewardID int64
	for _, entry := range pool {
		if choice < entry.Weight {
			rewardID = entry.ItemID
			break
		}
		choice -= entry.Weight
	}
	def, err := resolve(rewardID)
	if err != nil {
		return MonsterCardResult{}, err
	}
	if !def.IsCard || def.ItemID != rewardID || def.PVFPath == "" || def.Rarity != resultRarity || !cardStageValid(def.Stages, 0) {
		return MonsterCardResult{}, fmt.Errorf("合成奖励不符合实际PVF卡片定义")
	}
	changed := map[int16]bool{cmd.BinderSlot: true}
	if err = consumeCard(record.Slots, cmd.BinderSlot, 1); err != nil {
		return MonsterCardResult{}, err
	}
	preferred := cmd.InputSlots[0]
	for slot, count := range counts {
		if err = consumeCard(record.Slots, slot, count); err != nil {
			return MonsterCardResult{}, err
		}
		changed[slot] = true
		if slot < preferred {
			preferred = slot
		}
	}
	reward := dnfrepo.ItemStack{ItemID: rewardID, Count: 1, ExpireAt: def.ExpireAt, Extra: map[string]string{"item_kind": "stackable", "pvf_path": def.PVFPath}}
	dnfrepo.SetMonsterCardUpgradeCount(&reward, 0)
	reward.Extra["stack_limit"] = strconv.FormatInt(def.StackLimit, 10)
	if def.StackableType != "" {
		reward.Extra["stackable_type"] = def.StackableType
	}
	updateStackRawAmount(&reward)
	resultSlot, err := placeCard(&record, reward, def, preferred, state)
	if err != nil {
		return MonsterCardResult{ErrorCode: 4, Reason: err.Error(), ResultSlot: -1}, nil
	}
	changed[resultSlot] = true
	record.UpdatedAt = time.Now()
	if err = dnfrepo.SaveInventoryFields(ctx, o.repo, record, dnfrepo.InventoryFieldSlots); err != nil {
		return MonsterCardResult{}, err
	}
	return MonsterCardResult{Applied: true, Upgraded: upgraded, ItemID: rewardID, Level: 0, ChancePercent: percent, ResultSlot: resultSlot, ChangedSlots: cardChangedSlots(changed), Inventory: record}, nil
}

func cardChangedSlots(changed map[int16]bool) []int16 {
	slots := make([]int16, 0, len(changed))
	for slot := range changed {
		slots = append(slots, slot)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
	return slots
}
