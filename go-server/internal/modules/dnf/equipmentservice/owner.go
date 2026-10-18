// Package equipmentservice 负责装备跨界与再封装的规则和资产原子提交。
package equipmentservice

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/itemlock"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/repository"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrOwner = errors.New("装备服务仓储或账号不可用")

type Rejection struct {
	Code   byte
	Reason string
}

func (e Rejection) Error() string           { return e.Reason }
func reject(code byte, reason string) error { return Rejection{code, reason} }
func FailureCode(err error) byte {
	var r Rejection
	if errors.As(err, &r) {
		return r.Code
	}
	return 7
}

type Definition struct {
	ItemID                             uint32
	PVFPath, AttachType, EquipmentType string
	Equipment                          bool
	MinimumLevel, Rarity, RandomOption int64
	HasGrowthType, PeriodEquipment     bool
	CargoTransportStone                bool
	CargoTransportStoneType            int64
	ResealOneShot                      bool
	ResealLevelMin, ResealLevelMax     int64
	EpicPieceID                        uint32
	Policy                             itemplacement.SlotPolicy
}
type Catalog interface {
	Resolve(uint32) (Definition, error)
}
type Slot struct {
	List  byte
	Index int16
	Stack repository.ItemStack
}
type Result struct {
	Changed                             []Slot
	MaterialSlot, TargetSlot, CargoSlot int16
	MaterialRemaining                   int64
	Mode                                byte
	RecipientID                         string
	EpicPieceID, EpicBalance            uint32
}
type Command struct {
	AccountID, CharacterID, RecipientID string
	TargetSlot, MaterialSlot            int16
	TargetItemID                        uint32
	Mode                                byte
	Now                                 time.Time
	Encode                              func(byte, int16, repository.ItemStack) repository.ItemStack
}
type Owner struct {
	tx       repository.EquipmentServiceUnitOfWork
	settings repository.SettingsRepository
}

func NewOwner(g repository.Group) (*Owner, error) {
	tx, ok := g.MailboxAssets.(repository.EquipmentServiceUnitOfWork)
	if !ok {
		return nil, ErrOwner
	}
	return &Owner{tx, g.Settings}, nil
}
func slotKey(slot int16) string { return fmt.Sprintf("0:%d", slot) }
func timeFor(c Command) time.Time {
	if c.Now.IsZero() {
		return time.Now().UTC()
	}
	return c.Now.UTC()
}
func usable(s repository.ItemStack, now time.Time) bool {
	if !s.ExpireAt.IsZero() && !now.Before(s.ExpireAt) {
		return false
	}
	if itemlock.IsLockedAt(s, now) {
		return false
	}
	for _, k := range []string{"expire_time", "expire_unix"} {
		v := strings.TrimSpace(s.Extra[k])
		if v != "" {
			n, e := strconv.ParseInt(v, 10, 64)
			if e != nil || n < 0 || (n > 0 && now.Unix() >= n) {
				return false
			}
		}
	}
	return true
}
func getTarget(c Command, i repository.InventoryRecord, catalog Catalog) (repository.ItemStack, Definition, error) {
	s, ok := i.Slots[slotKey(c.TargetSlot)]
	if !ok || s.ItemID <= 0 || s.ItemID > math.MaxUint32 {
		return s, Definition{}, reject(0x11, "来源装备不存在")
	}
	d, e := catalog.Resolve(uint32(s.ItemID))
	if e != nil {
		return s, d, e
	}
	if !d.Equipment || s.Count != 1 || !d.Policy.ForInventory(&i).Contains(0, c.TargetSlot) || (c.TargetItemID != 0 && uint32(s.ItemID) != c.TargetItemID) {
		return s, d, reject(7, "来源不是开放装备格中的单件装备")
	}
	if !usable(s, timeFor(c)) {
		return s, d, reject(0xd6, "装备已锁定或到期")
	}
	if len(s.RawEntry) != 0 && len(s.RawEntry) != 119 {
		return s, d, reject(7, "装备实例行版本不匹配")
	}
	if len(s.RawEntry) == 119 && binary.LittleEndian.Uint32(s.RawEntry[2:6]) != uint32(s.ItemID) {
		return s, d, reject(7, "装备原始行与物品编号不匹配")
	}
	if p := s.Extra["pvf_path"]; p != "" && !strings.EqualFold(strings.ReplaceAll(p, "\\", "/"), d.PVFPath) {
		return s, d, reject(7, "装备实例与PVF定义不匹配")
	}
	return s, d, nil
}
func encode(c Command, list byte, index int16, s repository.ItemStack) repository.ItemStack {
	if c.Encode != nil && s.ItemID > 0 && s.Count > 0 {
		return c.Encode(list, index, s)
	}
	return s
}
func changed(c Command, slots map[string]repository.ItemStack, list byte, index int16, r *Result) {
	key := fmt.Sprintf("%d:%d", list, index)
	s := slots[key]
	if s.Count > 0 {
		s = encode(c, list, index, s)
		slots[key] = s
	}
	for n := range r.Changed {
		if r.Changed[n].List == list && r.Changed[n].Index == index {
			r.Changed[n].Stack = s
			return
		}
	}
	r.Changed = append(r.Changed, Slot{list, index, s})
}

// consume 只认真实分类的开放槽；账号灵魂必须来自唯一固定格。
func consume(c Command, catalog Catalog, i *repository.InventoryRecord, a *repository.AccountInventoryRecord, id uint32, n int64, r *Result) error {
	if n <= 0 || id == 0 {
		return ErrRules
	}
	if fixed, ok := repository.AccountWarehouseSlotForItem(int64(id)); ok {
		key := slotKey(fixed)
		s := a.Slots[key]
		if s.ItemID != int64(id) || s.Count < n || !usable(s, timeFor(c)) {
			return reject(0x16, "所需账号材料不足")
		}
		s.Count -= n
		if s.Count == 0 {
			delete(a.Slots, key)
		} else {
			a.Slots[key] = s
		}
		changed(c, a.Slots, 0, fixed, r)
		return nil
	}
	d, e := catalog.Resolve(id)
	if e != nil {
		return e
	}
	if d.Equipment {
		return ErrRules
	}
	policy := d.Policy.ForInventory(i)
	keys := []int16{}
	left := n
	for key, s := range i.Slots {
		var list int
		var slot int16
		if _, e := fmt.Sscanf(key, "%d:%d", &list, &slot); e != nil || list != 0 || key != slotKey(slot) {
			continue
		}
		if s.ItemID == int64(id) && s.Count > 0 && policy.Contains(0, slot) && usable(s, timeFor(c)) {
			keys = append(keys, slot)
		}
	}
	sort.Slice(keys, func(x, y int) bool { return keys[x] < keys[y] })
	for _, slot := range keys {
		s := i.Slots[slotKey(slot)]
		used := s.Count
		if used > left {
			used = left
		}
		s.Count -= used
		left -= used
		if s.Count == 0 {
			delete(i.Slots, slotKey(slot))
		} else {
			i.Slots[slotKey(slot)] = s
		}
		changed(c, i.Slots, 0, slot, r)
		if left == 0 {
			return nil
		}
	}
	return reject(0x16, "所需背包材料不足")
}

func (o *Owner) run(ctx context.Context, c Command, apply func(repository.Group, repository.CharacterRecord, repository.CharacterRecord, *repository.InventoryRecord, *repository.AccountInventoryRecord, *Result) error) (Result, error) {
	if o == nil || o.tx == nil || strings.TrimSpace(c.AccountID) == "" || strings.TrimSpace(c.CharacterID) == "" {
		return Result{}, ErrOwner
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dest := c.RecipientID
	if dest == "" {
		dest = c.CharacterID
	}
	var result Result
	err := o.tx.WithinEquipmentServiceAssets(ctx, c.AccountID, c.CharacterID, dest, func(g repository.Group, source, destination repository.CharacterRecord) error {
		i, found, e := g.Inventory.Load(ctx, c.CharacterID)
		if e != nil {
			return e
		}
		if !found || i.CharacterID != c.CharacterID {
			return ErrOwner
		}
		a, _, e := g.AccountInventory.Load(ctx, c.AccountID)
		if e != nil {
			return e
		}
		i = repository.CloneInventory(i)
		a = repository.CloneAccountInventory(a)
		a.AccountID = c.AccountID
		if a.Slots == nil {
			a.Slots = map[string]repository.ItemStack{}
		}
		if e = repository.BindInventoryContainerState(ctx, o.settings, &i); e != nil {
			return e
		}
		r := Result{MaterialSlot: c.MaterialSlot, TargetSlot: c.TargetSlot, Mode: c.Mode}
		if e = apply(g, source, destination, &i, &a, &r); e != nil {
			return e
		}
		i.UpdatedAt = timeFor(c)
		a.UpdatedAt = timeFor(c)
		if e = repository.SaveInventoryFields(ctx, g.Inventory, i, repository.InventoryFieldSlots); e != nil {
			return e
		}
		if e = g.AccountInventory.Save(ctx, a); e != nil {
			return e
		}
		result = r
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func (o *Owner) Transfer(ctx context.Context, catalog Catalog, config TransferConfig, c Command) (Result, error) {
	if catalog == nil || c.Mode != 0 && c.Mode != 2 {
		return Result{}, reject(7, "不支持该装备跨界模式")
	}
	return o.run(ctx, c, func(g repository.Group, source, destination repository.CharacterRecord, i *repository.InventoryRecord, a *repository.AccountInventoryRecord, r *Result) error {
		target, def, e := getTarget(c, *i, catalog)
		if e != nil {
			return e
		}
		var rule TransferRule
		if c.Mode == 0 {
			material, ok := i.Slots[slotKey(c.MaterialSlot)]
			if !ok || material.ItemID <= 0 || material.ItemID > math.MaxUint32 || material.Count <= 0 || material.Count > 65536 {
				return reject(0x11, "跨界石不存在或数量不能编码")
			}
			stone, e := catalog.Resolve(uint32(material.ItemID))
			if e != nil {
				return e
			}
			if !stone.CargoTransportStone || !stone.Policy.ForInventory(i).Contains(0, c.MaterialSlot) || !usable(material, timeFor(c)) || c.MaterialSlot == c.TargetSlot {
				return reject(7, "材料不是当前开放格中的装备跨界石")
			}
			var exists bool
			rule, exists = config.Rules[stone.CargoTransportStoneType]
			if !exists {
				return ErrRules
			}
			if !allows(rule, def, target) {
				return reject(7, "该装备不符合跨界石规则")
			}
			account, found, e := g.Account.Load(ctx, c.AccountID)
			if e != nil {
				return e
			}
			if !found {
				return ErrOwner
			}
			capacity, e := strconv.ParseInt(account.Metadata["account_cargo_level"], 10, 16)
			if e != nil || capacity <= 0 {
				return reject(0x13, "账号金库未激活")
			}
			dest := int16(-1)
			for slot := int16(0); int64(slot) < capacity; slot++ {
				if s, ok := a.Slots[fmt.Sprintf("12:%d", slot)]; !ok || s.Count <= 0 {
					dest = slot
					break
				}
			}
			if dest < 0 {
				return reject(4, "账号金库空间不足")
			}
			delete(i.Slots, slotKey(c.TargetSlot))
			material.Count--
			if material.Count == 0 {
				delete(i.Slots, slotKey(c.MaterialSlot))
			} else {
				i.Slots[slotKey(c.MaterialSlot)] = material
			}
			a.Slots[fmt.Sprintf("12:%d", dest)] = target
			r.CargoSlot = dest
			r.MaterialRemaining = material.Count
			changed(c, i.Slots, 0, c.MaterialSlot, r)
			changed(c, i.Slots, 0, c.TargetSlot, r)
			changed(c, a.Slots, 12, dest, r)
			return nil
		}
		if destination.CharacterID == source.CharacterID {
			return reject(7, "跨界目标不能是来源角色")
		}
		var found bool
		rule, found = config.Rules[7]
		if !found {
			return ErrRules
		}
		if !allows(rule, def, target) {
			return reject(7, "该装备不符合歌兰蒂斯跨界规则")
		}
		category := equipmentCategory(def.EquipmentType)
		if category < 0 || category > 3 {
			return reject(7, "跨界装备分类不支持")
		}
		base, ok := multiply(rangeWeightFor(rule.GradeWeights, def.MinimumLevel), rule.RarityWeights[def.Rarity])
		if !ok {
			return ErrRules
		}
		weights := rule.SoulWeights[def.Rarity]
		if len(weights) != 4 {
			return ErrRules
		}
		var n int64
		if def.Rarity == 5 {
			// 现用EXE 0x3c67155对异界品质直接取chronicle cost，
			// 不乘等级、品质或-1占位权重；结晶仍按随后的一般费用计算。
			n = rule.ChronicleCost
			if n <= 0 {
				return ErrRules
			}
		} else {
			n, ok = multiply(base, weights[category])
			if !ok {
				return ErrRules
			}
		}
		if e = consume(c, catalog, i, a, rule.SoulItemIDs[def.Rarity], n, r); e != nil {
			return e
		}
		if rule.ElementItemID != 0 && len(rule.ElementWeights[def.Rarity]) != 4 {
			return ErrRules
		}
		if weights := rule.ElementWeights[def.Rarity]; len(weights) == 4 && weights[category] > 0 {
			n, ok = multiply(base, weights[category])
			if !ok {
				return ErrRules
			}
			if e = consume(c, catalog, i, a, rule.ElementItemID, n, r); e != nil {
				return e
			}
		}
		// 现用EXE 0x3c671ca..0x3c67233只对65级以上史诗收取碎片；
		// 账本按当前PVF映射到碎片ID，不新建装备ID账本。
		if def.Rarity == 4 && def.MinimumLevel > 65 {
			if category >= len(rule.EpicPieceWeight) || def.EpicPieceID == 0 {
				return ErrRules
			}
			n, ok = multiply(base, rule.EpicPieceWeight[category])
			if !ok || n > math.MaxUint32 {
				return ErrRules
			}
			account, found, e := g.Account.Load(ctx, c.AccountID)
			if e != nil {
				return e
			}
			if !found {
				return ErrOwner
			}
			key := repository.AccountEpicPieceMetadataKey(def.EpicPieceID)
			balance := repository.ParseAccountEpicPieceBalance(account.Metadata[key])
			if uint64(balance) < uint64(n) {
				return reject(0x16, "所需史诗碎片不足")
			}
			r.EpicPieceID = def.EpicPieceID
			r.EpicBalance = balance - uint32(n)
			if e = repository.SaveAccountMetadataEntry(ctx, g.Account, account, key, repository.FormatAccountEpicPieceBalance(r.EpicBalance), timeFor(c)); e != nil {
				return e
			}
		}
		for _, cost := range rule.ParticularCosts {
			if cost.Rarity == def.Rarity && cost.Level == def.MinimumLevel {
				if e = consume(c, catalog, i, a, cost.ItemID, cost.Count, r); e != nil {
					return e
				}
			}
		}
		delete(i.Slots, slotKey(c.TargetSlot))
		changed(c, i.Slots, 0, c.TargetSlot, r)
		_, e = repository.AppendSystemMail(ctx, g.Mailbox, repository.SystemMailDelivery{RecipientCharacterID: destination.CharacterID, Title: "装备跨界", Body: source.Name + "通过歌兰蒂斯寄送了一件装备，请领取附件。", Source: "grandi_equipment_transport", Attachments: []repository.MailAttachment{{ItemID: target.ItemID, Count: 1, Bind: target.Bind, ExpireAt: target.ExpireAt, RawEntry: append([]byte(nil), target.RawEntry...), Extra: target.Extra}}, CreatedAt: timeFor(c)})
		if e != nil {
			return e
		}
		r.RecipientID = destination.CharacterID
		return nil
	})
}
func multiply(a, b int64) (int64, bool) {
	if a <= 0 || b <= 0 || a > math.MaxInt64/b {
		return 0, false
	}
	return a * b, true
}
func equipmentCategory(t string) int {
	switch normalizeCargoEquipmentType(t) {
	case "weapon":
		return 0
	case "coat", "shoulder", "pants", "shoes", "waist":
		return 1
	case "amulet", "wrist", "ring":
		return 2
	case "support", "magic stone", "earring":
		return 3
	}
	return -1
}
func allows(r TransferRule, d Definition, s repository.ItemStack) bool {
	if !d.Equipment || r.ExcludeItemIDs[d.ItemID] || len(r.IncludeItemIDs) > 0 && !r.IncludeItemIDs[d.ItemID] {
		return false
	}
	if len(r.EquipmentTypes) > 0 && !r.EquipmentTypes[normalizeCargoEquipmentType(d.EquipmentType)] {
		return false
	}
	if len(r.GradeWeights) > 0 && rangeWeightFor(r.GradeWeights, d.MinimumLevel) <= 0 || len(r.RarityWeights) > 0 && r.RarityWeights[d.Rarity] <= 0 {
		return false
	}
	return !(r.ExceptGrowth && d.HasGrowthType || r.ExceptPeriod && (d.PeriodEquipment || itemHasPeriod(s)))
}

func SealFlag(s repository.ItemStack) byte {
	if len(s.RawEntry) == 119 {
		return s.RawEntry[13]
	}
	for _, k := range []string{"seal_flag", "seal", "sealed"} {
		if v, ok := s.Extra[k]; ok {
			n, e := strconv.ParseUint(v, 10, 8)
			if e == nil {
				return byte(n)
			}
		}
	}
	if s.Bind {
		return 1
	}
	return 0
}
func packed(s repository.ItemStack) byte {
	if len(s.RawEntry) == 119 {
		return s.RawEntry[10]
	}
	for _, k := range []string{"ext_data0", "packed_flag_byte"} {
		if v, ok := s.Extra[k]; ok {
			n, _ := strconv.ParseUint(v, 10, 8)
			return byte(n)
		}
	}
	return 0
}
func ResealCount(s repository.ItemStack) byte { return packed(s) >> 5 }
func (o *Owner) Reseal(ctx context.Context, catalog Catalog, config ResealConfig, c Command) (Result, error) {
	if catalog == nil || c.Mode > 1 {
		return Result{}, reject(7, "封装模式无效")
	}
	return o.run(ctx, c, func(g repository.Group, _, _ repository.CharacterRecord, i *repository.InventoryRecord, a *repository.AccountInventoryRecord, r *Result) error {
		target, d, e := getTarget(c, *i, catalog)
		if e != nil {
			return e
		}
		if SealFlag(target) != 0 {
			return reject(0x12, "装备已封装")
		}
		attach := normalizeCargoEquipmentType(d.AttachType)
		if attach != "sealing" && !(attach == "free" && d.RandomOption > 0) {
			return reject(7, "装备模板不支持再封装")
		}
		if d.PeriodEquipment || itemHasPeriod(target) {
			return reject(7, "期限装备不支持再封装")
		}
		count := ResealCount(target)
		if raw, exists := target.Extra["reseal_count"]; exists {
			recorded, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 8)
			if err != nil || recorded > 7 || byte(recorded) != count {
				return reject(7, "装备封装次数证据不一致")
			}
		}
		oneshot := false
		if c.Mode == 1 {
			if err := checkResealLimit(config, d, count); err != nil {
				return err
			}
			id, n, ok := config.materialAndCost(d, count)
			if !ok {
				return ErrRules
			}
			if e = consume(c, catalog, i, a, id, n, r); e != nil {
				return e
			}
			r.MaterialSlot = -1
		} else {
			wax, found := i.Slots[slotKey(c.MaterialSlot)]
			if !found || wax.ItemID <= 0 || wax.ItemID > math.MaxUint32 || wax.Count <= 0 || wax.Count > math.MaxUint32 {
				return reject(0x16, "封装蜜蜡不足")
			}
			wd, e := catalog.Resolve(uint32(wax.ItemID))
			if e != nil {
				return e
			}
			var valid bool
			oneshot, valid = WaxKind(wd)
			if !valid || !wd.Policy.ForInventory(i).Contains(0, c.MaterialSlot) || !usable(wax, timeFor(c)) || c.MaterialSlot == c.TargetSlot {
				return reject(7, "材料不是可用的封装蜜蜡")
			}
			if wd.ResealLevelMin > 0 && d.MinimumLevel < wd.ResealLevelMin || wd.ResealLevelMax > 0 && d.MinimumLevel > wd.ResealLevelMax {
				return reject(7, "装备超出蜜蜡等级范围")
			}
			if !oneshot {
				if err := checkResealLimit(config, d, count); err != nil {
					return err
				}
			}
			n, valid := config.waxCost(d, 0)
			if !valid {
				return ErrRules
			}
			if oneshot {
				n = 1
			} else {
				n, valid = config.waxCost(d, count)
				if !valid {
					return ErrRules
				}
			}
			if wax.Count < n {
				return reject(0x16, "封装蜜蜡不足")
			}
			wax.Count -= n
			r.MaterialRemaining = wax.Count
			if wax.Count == 0 {
				delete(i.Slots, slotKey(c.MaterialSlot))
			} else {
				i.Slots[slotKey(c.MaterialSlot)] = wax
			}
			changed(c, i.Slots, 0, c.MaterialSlot, r)
		}
		if !oneshot {
			count++
		}
		if target.Extra == nil {
			target.Extra = map[string]string{}
		}
		b := (packed(target) & 31) | (count << 5)
		target.Extra["ext_data0"] = strconv.Itoa(int(b))
		target.Extra["packed_flag_byte"] = strconv.Itoa(int(b))
		target.Extra["reseal_count"] = strconv.Itoa(int(count))
		target.Extra["seal_flag"] = "1"
		target.Extra["seal"] = "1"
		// 再封装开始新的使用周期，不能继续携带上次穿戴的解封标记。
		delete(target.Extra, "seal_removed_by_first_equip")
		delete(target.Extra, "trade_locked_by_first_equip")
		// 封装状态由seal字段表达；角色绑定必须解除，交易owner会拒绝Bind=true。
		target.Bind = false
		for _, key := range []string{"ext0", "packed_flag", "packed"} {
			delete(target.Extra, key)
		}
		if len(target.RawEntry) == 119 {
			target.RawEntry[10] = b
			target.RawEntry[13] = 1
		}
		i.Slots[slotKey(c.TargetSlot)] = target
		changed(c, i.Slots, 0, c.TargetSlot, r)
		return nil
	})
}

func itemHasPeriod(s repository.ItemStack) bool {
	if !s.ExpireAt.IsZero() {
		return true
	}
	for _, k := range []string{"expire_time", "expire_unix"} {
		if value := strings.TrimSpace(s.Extra[k]); value != "" {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n != 0 {
				return true
			}
		}
	}
	return false
}

// 此校验只用于封装，跨界不依赖剩余封装次数。一次性蜜蜡遵循原有不增次数契约。
func checkResealLimit(config ResealConfig, d Definition, count byte) error {
	limit := config.countLimit(d.MinimumLevel)
	if limit == 0 {
		return ErrRules
	}
	if count >= limit {
		return reject(0x0d, "装备剩余封装次数为0")
	}
	return nil
}
