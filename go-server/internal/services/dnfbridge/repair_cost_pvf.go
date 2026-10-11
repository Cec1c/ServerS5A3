package dnfbridge

import (
	"context"
	"fmt"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// currentRepairCostCatalog owns the runtime-PVF repair pricing inputs: the
// global rates from equipment/pricetable.tbl and etc/upgrade.etc, plus the
// per-item durability ([durability]/[maximum durability]/[max durability]) /
// [repair price]/[grade]/[equipment type] evidence.
// It mirrors the 86JP EquipmentRepairPriceProvider inputs but reads only the
// active runtime PVF. Per-item evidence is cached; a negative MaxDurability
// (-1) marks a known non-repairable (or unknown) item without erroring.
type currentRepairCostCatalog struct {
	repairCostRate  float64
	quickRepairRate float64
	upgradeRates    []float64
	source          initialEquipmentTextSource
	paths           map[int64]string
	evidence        map[int64]alignedcmd.RepairCostEvidence
}

func (s *Service) currentRepairCostCatalog() (*currentRepairCostCatalog, error) {
	if s == nil {
		return nil, fmt.Errorf("repair cost catalog: service required")
	}
	s.repairCostMu.Lock()
	defer s.repairCostMu.Unlock()
	if s.repairCostCatalog != nil || s.repairCostLoadErr != nil {
		return s.repairCostCatalog, s.repairCostLoadErr
	}
	if err := s.preloadEquipmentStatIndex(context.Background()); err != nil {
		s.repairCostLoadErr = fmt.Errorf("repair cost catalog: equipment index: %w", err)
		return nil, s.repairCostLoadErr
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		s.repairCostLoadErr = fmt.Errorf("repair cost catalog: PVF archive: %w", err)
		return nil, s.repairCostLoadErr
	}
	s.equipmentStatsMu.Lock()
	paths := s.equipmentStatPaths
	s.equipmentStatsMu.Unlock()
	if len(paths) == 0 {
		s.repairCostLoadErr = fmt.Errorf("repair cost catalog: equipment paths unavailable")
		return nil, s.repairCostLoadErr
	}
	catalog, err := newCurrentRepairCostCatalog(archive, paths)
	if err != nil {
		s.repairCostLoadErr = err
		return nil, err
	}
	s.repairCostCatalog = catalog
	return catalog, nil
}

func newCurrentRepairCostCatalog(source initialEquipmentTextSource, paths map[int64]string) (*currentRepairCostCatalog, error) {
	if source == nil {
		return nil, fmt.Errorf("repair cost catalog: PVF source required")
	}
	priceText, err := source.ReadText("equipment/pricetable.tbl")
	if err != nil {
		return nil, fmt.Errorf("repair cost catalog: read equipment/pricetable.tbl: %w", err)
	}
	priceDoc, err := dnfpvf.Parse("equipment/pricetable.tbl", priceText)
	if err != nil {
		return nil, fmt.Errorf("repair cost catalog: parse equipment/pricetable.tbl: %w", err)
	}
	repairRates := priceDoc.Numbers("repair cost")
	if len(repairRates) == 0 || repairRates[0] <= 0 {
		return nil, fmt.Errorf("repair cost catalog: pricetable.tbl [repair cost] missing")
	}
	quickRates := priceDoc.Ints("quick repair cost rate")
	if len(quickRates) == 0 || quickRates[0] <= 0 {
		return nil, fmt.Errorf("repair cost catalog: pricetable.tbl [quick repair cost rate] missing")
	}
	upgradeText, err := source.ReadText("etc/upgrade.etc")
	if err != nil {
		return nil, fmt.Errorf("repair cost catalog: read etc/upgrade.etc: %w", err)
	}
	upgradeDoc, err := dnfpvf.Parse("etc/upgrade.etc", upgradeText)
	if err != nil {
		return nil, fmt.Errorf("repair cost catalog: parse etc/upgrade.etc: %w", err)
	}
	upgradeRateInts := upgradeDoc.Ints("repair cost rate by upgrade level")
	upgradeRates := make([]float64, 0, len(upgradeRateInts))
	for _, value := range upgradeRateInts {
		upgradeRates = append(upgradeRates, float64(value))
	}
	return &currentRepairCostCatalog{
		repairCostRate:  repairRates[0],
		quickRepairRate: float64(quickRates[0]) / 100,
		upgradeRates:    upgradeRates,
		source:          source,
		paths:           paths,
		evidence:        make(map[int64]alignedcmd.RepairCostEvidence),
	}, nil
}

func (c *currentRepairCostCatalog) resolve(itemID int64) (alignedcmd.RepairCostEvidence, error) {
	if evidence, ok := c.evidence[itemID]; ok {
		return evidence, nil
	}
	evidence := alignedcmd.RepairCostEvidence{
		MaxDurability:   -1,
		RepairCostRate:  c.repairCostRate,
		QuickRepairRate: c.quickRepairRate,
		UpgradeRates:    c.upgradeRates,
	}
	refPath := strings.TrimSpace(c.paths[itemID])
	if refPath == "" {
		// Unknown item: marked non-repairable, never an error (86JP skip rule).
		c.evidence[itemID] = evidence
		return evidence, nil
	}
	text, actualPath, err := readInitialPVFText(c.source, initialPVFPath("equipment", refPath), refPath)
	if err != nil {
		return alignedcmd.RepairCostEvidence{}, fmt.Errorf("repair cost catalog: item=%d path=%q: %w", itemID, refPath, err)
	}
	document, err := dnfpvf.Parse(actualPath, text)
	if err != nil {
		return alignedcmd.RepairCostEvidence{}, fmt.Errorf("repair cost catalog: item=%d path=%q: %w", itemID, actualPath, err)
	}
	// 耐久真源与 dungeon_drop_catalog.go:244 / initial_equipment_metadata.go:50 /
	// gmtool/api_items_durability.go:90 完全同口径：[durability] →
	// [maximum durability] → [max durability]，取第一个 > 0 的值。
	// 修复前这里只读 [durability]：只声明备用键的装备会得到 MaxDurability = -1，
	// 现场表现为"耐久能补满、却永远报不可修理"（changlog.md:3421 记录的缺口，
	// 消费方 equip/owner.go:250 与 inventory/owner.go:1089 都按 <= 0 判不可修）。
	//
	// 有意**不**做上限 clamp：RepairCostEvidence.MaxDurability 是 int64，
	// dungeon_drop_catalog 之所以 clamp 到 65535 只是因为它的字段是 uint16。
	// 原实现命中 [durability] 时原样透传，这里引入 clamp 会改变这批**既有已支持**
	// 装备的定价（equip/repair_cost.go 按 price*rate/maxDurability*lost 计费），
	// 与"有 [durability] 时行为逐字节不变"冲突；三键统一不 clamp 也避免同一份
	// 文档换个键名就得到不同价格。真实 PVF 的耐久都是两位数，边界不构成实际风险。
	//
	// [durability] 存在且为正时，赋值结果与修复前逐字节一致；只有 [durability]
	// 缺失或非正时才会落到备用键。三个键都没有、或都非正时仍是 -1（不可修哨兵）。
	if durability, found := firstPositivePVFInt(document, "durability", "maximum durability", "max durability"); found {
		evidence.MaxDurability = durability
	}
	if repairPrice, found := document.Int("repair price"); found {
		evidence.RepairPrice = repairPrice
	}
	if grade, found := document.Int("grade"); found {
		evidence.Grade = grade
	}
	if equipmentType, found := document.Text("equipment type"); found {
		evidence.EquipmentType = normalizeEquipmentPlacementPVFType(equipmentType)
	}
	c.evidence[itemID] = evidence
	return evidence, nil
}

// firstPositivePVFInt 按给定顺序返回第一个存在且 > 0 的 section 首整数。
//
// 耐久（max durability）在运行时 PVF 里有三个同义键，历史文档写法不一：
// [durability] / [maximum durability] / [max durability]。全包统一走这个口径
// （dungeon_drop_catalog.go:244、initial_equipment_metadata.go:50、
// gmtool/api_items_durability.go:90、repair_cost_pvf.go 的修理定价、
// magic_box_pvf.go:436 的魔盒奖励模板），避免同一个道具在两条路径上
// 得到"可修"与"不可修"两种结论。
func firstPositivePVFInt(document *dnfpvf.Document, names ...string) (int64, bool) {
	for _, name := range names {
		if value, found := document.Int(name); found && value > 0 {
			return value, true
		}
	}
	return 0, false
}

// alignedRepairCostResolverForCommand keeps PVF loading request-driven: only
// the repair command opens the catalog. A nil resolver makes every paid
// repair fail closed in the owners.
func (s *Service) alignedRepairCostResolverForCommand(opcode dnfenum.CmdPacket) (alignedcmd.RepairCostResolver, error) {
	if opcode != dnfenum.CmdPacketRepairEquipment {
		return nil, nil
	}
	catalog, err := s.currentRepairCostCatalog()
	if err != nil {
		return nil, err
	}
	return catalog.resolve, nil
}
