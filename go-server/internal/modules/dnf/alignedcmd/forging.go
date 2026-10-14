// 本文件定义 op438「锻造 / 别升级（separate upgrade）」在桥接层与领域层之间的
// 依赖中性契约：请求形状、运行时 PVF 解析结果、以及领域层回给桥接层的执行结果。
//
// 这里只有数据形状，不含协议字节与 PVF 解析：
//
//	请求字节（u8 list / u16 slot / u32 templateId / u16 materialSlot /
//	长度前缀名字 / u8 confirmation）由 dnfbridge/forging_codec.go 解出，
//	运行时 PVF 事实由 dnfbridge/forging_pvf.go 解析，
//	class1 438 回包体由 dnfbridge/forging_codec.go 组装，
//	真正的资产变更（材料消耗 + 锻造等级写入 + 回滚）由
//	modules/dnf/inventory 的 owner 在同一个角色物品事务里完成。
package alignedcmd

// ForgingRequest 是当前 EXE op438 请求体解出来的结果。
// Confirmation 只做取证：发送端把它当函数参数传入，全 EXE 扫不到取值域
// （见 Docs/D33_FORGING_20261014.md），因此任何字节都必须被接受。
type ForgingRequest struct {
	List         byte
	Slot         int16
	ItemID       uint32
	MaterialSlot int16
	Name         []byte
	Confirmation byte
}

// ForgingLevelRow 是 etc/upgrade_separate.etc `[table]` 里的一档。
// TargetLevel = 序号 + 1；SuccessWeight 的满值是 10000（该玩法掷点是
// rand(10000)），MaterialWeight 是材料数量的乘算权重。
type ForgingLevelRow struct {
	TargetLevel    int
	SuccessWeight  int
	MaterialWeight float64
}

// ForgingMaterialRule 是 `[item weights by grade]` 的三元组
// （装备品级 → 材料模板 + 基础数量）。
type ForgingMaterialRule struct {
	ItemTemplateID int
	BaseCount      int
}

// ForgingTicketDefinition 是材料槽那一件 stackable 的券参数
// （PVF `[equipment separate reinforcement ticket]`，或幸运锻造券
// `[action type] [lucky enchant deed] 2`）。
type ForgingTicketDefinition struct {
	// TargetLevel 是 fixed 档的绝对目标等级、additional 档的增量。
	TargetLevel int
	// SuccessWeight 满值 10000（`[equipment separate reinforcement ticket]`
	// 的百分数 ×100；幸运锻造券按物品说明取 5000 = 50%）。
	SuccessWeight int
	IsFixed       bool
	IsAdditional  bool
	// ApplyValue 是 additional 档允许的**起始**等级上界（<0 = 只用
	// MaxLevel - TargetLevel 兜底），语义见厂商
	// InventorySeparateUpgradeService.cs:93-101。
	ApplyValue int
	// FailureDecrementLevel 是失败时下降的锻造阶段数（0 = 失败保持不变，
	// 当前真档里的幸运锻造券就是 0）。
	FailureDecrementLevel int
	PVFPath               string
}

// ForgingResolution 是材料/目标这一对在运行时 PVF 里的完整事实。
type ForgingResolution struct {
	// TablePVFPath 是等级/材料表来源（取证用）。
	TablePVFPath string
	// MaxLevel 是 `[separate upgrade max]`（真档 8）——锻造的等级/次数上限。
	MaxLevel int
	// Levels 按 TargetLevel 升序（TargetLevel = 序号 + 1）。
	Levels []ForgingLevelRow
	// MaterialsByGrade 的键是装备 PVF `[grade]`。
	MaterialsByGrade map[int]ForgingMaterialRule
	// ItemWeightsByRarity 按装备 PVF `[rarity]` 索引（真档 7 个）。
	ItemWeightsByRarity []float64

	// TargetIsWeapon 表示目标装备的 `[equipment type]` 是武器
	// （厂商 EquipmentTypeInfo.IsWeapon；本仓运行时 PVF token 为 weapon）。
	TargetIsWeapon bool
	// TargetGrade/TargetRarity 是目标装备 PVF `[grade]` / `[rarity]`。
	TargetGrade  int
	TargetRarity int
	// TargetDurability 是目标装备 PVF `[durability]`（厂商 separate-upgrade
	// 要求实例耐久等于它，即"满耐久才能锻造"）。
	TargetDurability int64
	TargetPVFPath    string

	// MaterialItemID 是解析器实际分类的那一件材料模板 id（必须与领域层从权威
	// 背包里读出来的那一行逐字相同，否则领域层拒绝本次请求）。
	MaterialItemID int
	// MaterialPVFPath 是材料槽那一件的 stackable 路径（取证用）。
	MaterialPVFPath string
	// PortableFurnace 表示材料槽那一件是便携式锻造炉
	// （`[action type] [portable genuine damage upgrade upgrede]`）：
	// 消耗 1 个炉子、**不消耗** 3326，这是它唯一的改动。
	PortableFurnace bool
	// Ticket 非零（TargetLevel > 0）表示材料槽那一件是锻造券。
	Ticket ForgingTicketDefinition
	// TicketWhitelist 是券的 `[reinforcement usable item list]`（真档 125 件券
	// 全部带这一段）。非空即"只有名单里的目标能用这张券"。
	TicketWhitelist []int64
}

// ForgingTicketUsable 报告这次材料槽里的东西是不是一张可用的锻造券。
func (r ForgingResolution) ForgingTicketUsable() bool {
	return r.Ticket.TargetLevel > 0 && (r.Ticket.IsFixed || r.Ticket.IsAdditional)
}

// ForgingLevelFor 返回目标等级那一档；越界/表长不足时 ok=false
// （调用方按"不支持"拒绝，绝不臆造成功率）。
func (r ForgingResolution) ForgingLevelFor(targetLevel int) (ForgingLevelRow, bool) {
	if targetLevel < 1 || targetLevel > len(r.Levels) || targetLevel > r.MaxLevel {
		return ForgingLevelRow{}, false
	}
	return r.Levels[targetLevel-1], true
}

// ForgingResolver 由 dnfbridge 提供：把材料模板 + 目标模板翻成运行时 PVF 事实。
// 材料/目标是领域层从权威背包里读出来的真实模板 id（不是请求里的原始字节），
// 所以解析结果不能被请求体或库存 Extra 元数据冒充。
type ForgingResolver func(materialItemID int64, targetItemID int64) (ForgingResolution, error)

// ForgingOutcome 是 op438 这一次的领域结果。dnfbridge 只读它组包：
//
//	Rejected=true  → class1 失败体 0 + u8 ErrorCode（客户端先复位锻造 UI）；
//	Rejected=false → class1 成功体 13 字节（Succeeded 决定 outcome 0/1）。
//
// OldLevel/NewLevel/TargetList/TargetSlot/MaterialRemaining 与成功体逐字对应。
type ForgingOutcome struct {
	Rejected          bool
	ErrorCode         byte
	TargetList        byte
	TargetSlot        int16
	OldLevel          byte
	Succeeded         bool
	NewLevel          byte
	MaterialSlot      int16
	MaterialRemaining uint32
	// MaterialConsumed / PortableFurnace / TicketApplied / LevelChanged 只用于
	// 日志取证，不上行。
	MaterialConsumed int64
	PortableFurnace  bool
	TicketApplied    bool
	LevelChanged     bool
}
