// 本文件定义 DNF 已对齐命令的模块化分流结果。
// 这里只承载协议分流和回包描述，不持有具体玩法状态。
package alignedcmd

import (
	"context"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/experiencebuff"
	dnfplacement "longheng.io/server/internal/modules/dnf/itemplacement"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

// Request 是 dnfbridge 交给 DNF 业务模块的最小上下文。
// Body 是当前包体副本，模块不得在原地修改调用方缓冲区。
type Request struct {
	Command                byte
	CommandKnown           bool
	Opcode                 uint16
	Body                   []byte
	AccountID              string
	SelectedCharacterID    uint16
	GuardianShield         GuardianShieldResolver
	Repositories           dnfrepo.Group
	EquipmentPlacement     EquipmentPlacementValidator
	InventoryItemPlacement func(dnfrepo.ItemStack) (dnfplacement.SlotPolicy, error)
	InventoryCargoInsert   func(dnfrepo.ItemStack, byte) error
	AvatarClassifier       AvatarClassifier
	NameTagChecker         func(itemID uint32) bool
	PetHatchResolver       PetHatchResolver
	EnchantBeadResolver    EnchantBeadResolver
	AmplifyItemResolver    AmplifyItemResolver
	RandomOptionResolver   RandomOptionResolver
	UpgradeTicketResolver  UpgradeTicketResolver
	UpgradePolicyResolver  UpgradePolicyResolver
	UpgradeCostResolver    UpgradeCostResolver

	// GuildMedalUpgradeResolver resolves only the current list38 flag sender.
	GuildMedalUpgradeResolver GuildMedalUpgradeResolver
	// ForgingResolver 让 op438（锻造 / 别升级 separate upgrade）拿到这一次
	// 材料/目标在**运行时 PVF** 里的事实：etc/upgrade_separate.etc 的等级表与
	// 材料表、目标的武器/品级/稀有度/耐久、材料槽那一件是不是便携式锻造炉或
	// （券参数 + 目标白名单）。为零 = 该命令必须失败关闭（不回成功 ACK）。
	// 语义与取证见 dnfbridge/forging_pvf.go 与 Docs/D33_FORGING_20261014.md。
	ForgingResolver ForgingResolver
	// Forging 是 dnfbridge 用当前 EXE 的 438 编解码器解出来的锻造请求
	// （协议形状归桥接层，见 dnfbridge/forging_codec.go）。为零 = 该命令的请求体
	// 没有被解出来，领域层按"目标无效"回失败体，绝不猜坐标。
	Forging *ForgingRequest
	// UpgradeProtectTicketResolver 让 op50 的"失败即摧毁"档能按运行时 PVF
	// 认出背包里的装备强化/增幅保护券（`[action type] [protect equipment]` /
	// `[amplify protect equipment]`）。为零时该档保持改动前的行为（直接摧毁），
	// 因此所有既有调用方与测试无需改动。见 inventory/upgrade_protect_ticket.go。
	UpgradeProtectTicketResolver UpgradeProtectTicketResolver
	// UpgradeRateBuffResolver 让 op44 认出「强化/增幅成功率 buff 药水」
	// （`[stackable type] [item upgrad buff usual upgrade]` / `… amplify upgrade]`），
	// 并把 PVF `[upgrade prob increase]` 的十万分比加成与 `[stat change duration]`
	// 的持续时间交给领域层记账。为零 = 不记账（既有行为：药水只被消耗，成功率不变）。
	// 见 inventory/upgrade_rate_buff.go。
	ExperienceBuffResolver     func(int64) (experiencebuff.Effect, error)
	ExperienceBuffNow          time.Time
	UpgradeRateBuffResolver    UpgradeRateBuffResolver
	MagicBoxResolver           MagicBoxResolver
	MagicBoxRewardItemResolver MagicBoxRewardItemResolver
	RandomRewardItemResolver   RandomRewardItemResolver
	PremiumContractResolver    PremiumContractResolver
	// StackableUseProvenanceResolver 让 op44/op251 的"这一格能不能右键使用"判据以
	// **运行时 PVF** 为准，而不是库存表里的 stackable_type 元数据列（授予链路上有一批
	// 行只写了 item_kind + pvf_path，连 [waste] 药水都会被误拒；线上实例
	// 10007484 她的心意）。为零 = 逐字节回到改动前的元数据判据。
	// 见 inventory/use_upgrad_buff.go 与 dnfbridge/stackable_use_provenance_pvf.go。
	StackableUseProvenanceResolver StackableUseProvenanceResolver
	DamageFontResolver             DamageFontResolver
	DamageFontNow                  time.Time
	RepairCostResolver             RepairCostResolver
	MailboxItemResolver            MailboxItemResolver
	// EpicPieceCreditor 让"领取邮件附件"这条会写背包的路径把史诗碎片改记到
	// **账号级**史诗图鉴账本（碎片不占背包任何格）。为零时 mail 模块保持改动前的
	// 行为（碎片照旧当普通 [material] 进材料页），因此所有既有调用方无需改动。
	// 语义与边界见 dnfbridge/epic_piece_grant.go。
	EpicPieceCreditor MailboxEpicPieceCreditor
	// WarehouseCreditor 让"领取邮件附件"这条会写背包的路径把小晶块/灵魂改写到
	// **账号级晶块仓库**的固定格（354..365），不占背包任何格。为零时 mail 模块
	// 保持改动前的行为（晶块当普通 [material] 堆进材料页/快捷格），因此所有既有
	// 调用方无需改动。语义与边界见 dnfbridge/cube_warehouse_routing.go。
	WarehouseCreditor  MailboxWarehouseCreditor
	SkillCatalog       *dnfskill.Table
	InitialSkillLevels map[uint16]int
	SkillPointBaseline *dnfrepo.SkillPointState
	Party              *PartyState
	// LoginWearWindowOpen 由 dnfbridge 填：本条 op19 落在"进入场景后的登录引导
	// 窗口"内（窗口界定见 dnfbridge/equip_login_wear_window.go）。它只被
	// DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP（默认关闭）这条开关读取；默认档下
	// 业务模块完全忽略它，行为与历史一致。
	LoginWearWindowOpen bool
	// WornSlotClaimOccurrence 由 dnfbridge 填：本条 op19 若属于"声明装备槽穿戴态"那一族,
	// 它就是该装备槽在**当前登录纪元**里的第几条（1 起算）；0 = 桥侧未计数（测试字面量、
	// 或"全部吞掉"档）= 行为与今天逐字节一致。语义、epoch 边界与开关见
	// dnfbridge/equip_worn_slot_claim_occurrence.go。
	WornSlotClaimOccurrence int
	// MirrorReequipEcho 由 dnfbridge 填：本条 op19 是"客户端在我们刚把那件脱下之后立刻
	// 发来的镜像回穿包"（同一件 + 同一装备槽 + 窗口内）。域侧据此判 ClientEcho noop，
	// 见 dnfbridge/equip_mirror_requip_echo.go。
	MirrorReequipEcho bool
	// PlayerRequestFlag 由 dnfbridge 填：本条 op19 的尾字节 body[26]==1 = 客户端模块 8 标注的
	// "玩家亲手发起"。域侧据此让 worn_slot_reassert 那一族门禁惰性。见
	// dnfbridge/equip_player_request_flag.go。
	PlayerRequestFlag bool
	// LoginWearReassertNoopRefresh 由 dnfbridge 填：DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP_REFRESH
	// 的归一化结果（默认 = on）。它为真时，命中 equipmentLoginWearReassertNoop 的
	// noop 分支（什么都不写库）也要照发真落库路径那份权威刷新
	// （PostActionRefreshSelectedWearContainers + PostActionRefreshSelectedActorAppearance
	// + 战力 413），让客户端那份没被纠正过的本地穿戴视图收敛、不再重发该族 op19。
	// **零值 false = 今天的行为（只回一条 msg_id=19 ACK）**，因此不经过 dnfbridge 的
	// 构造路径（测试与探针里的 Request 字面量）逐字节等于改动前。证据见
	// dnfbridge/equip_login_wear_family_window.go 与 _dist/weapon-unequip-diagnosis.md §3.3。
	LoginWearReassertNoopRefresh bool
	// EquipAppearanceSlotGateOff 由 dnfbridge 填：DNFBRIDGE_EQUIP_APPEARANCE_SLOT_GATE
	// 的归一化结果（默认 = on = 只在"真的会改变 mode0 纸娃娃外观"的穿脱上补
	// PostActionRefreshSelectedActorAppearance；只有显式 off 才回退到"每次穿脱都发"）。
	// 零值 false = 修复档（外观相关性闸门生效），因此所有不经过 dnfbridge 的构造路径
	// （测试与探针里的 Request 字面量）与线上默认逐字节一致。见
	// dnfbridge/equip_move_refresh.go 的 equipAppearanceSlotGateEnv。
	EquipAppearanceSlotGateOff bool
	// EquipmentInventorySlotRefresh requests repository-backed incremental
	// bag rows after a committed wear move. The bridge enables this only for
	// the verified current-client profile; no-op wear claims emit no bag row.
	EquipmentInventorySlotRefresh bool
}

// EquipmentPlacementRequest is the dependency-neutral bridge contract used
// by the inventory module before an item enters a worn equipment slot.
type EquipmentPlacementRequest struct {
	CharacterID     string
	ItemID          int64
	SourceListType  byte
	SourceSlotIndex int16
	TargetSlotIndex int16
}

// EquipmentPlacementValidator keeps current-PVF access in dnfbridge while
// allowing the equipment owner to enforce the check inside its item UOW.
type EquipmentPlacementValidator func(context.Context, EquipmentPlacementRequest) error

// AvatarClassifier 用活动运行时 PVF 判定一件装备模板是不是装扮、以及是不是
// 「稀有克隆装扮」。isAvatar 只在它落在 actor 外观槽时为真；isClone 另外要求
// PVF `[item category]` 里出现 `clear avatar`。名字和固定 item id 表都不作为
// 判据（克隆装扮的模板本身就是透明件，外观来自穿上时记下的被克隆件）。
//
// 同族参考实现见 90原神开源0.1.8 `alignedcmd/handler.go:61-64`。为零时
// equip 域完全不写克隆外观键，行为与改动前逐字节一致。
type AvatarClassifier func(itemID uint32) (isAvatar bool, isClone bool)

// PetHatchResolution is the dependency-neutral runtime-PVF result passed from
// dnfbridge into the pet domain. Keeping this DTO here avoids an alignedcmd ->
// pet import cycle while still preventing request-body or inventory-Extra
// values from becoming authoritative hatch mappings.
type PetHatchResolution struct {
	EggItemID      int64
	HatchedItemID  int64
	EggPVFPath     string
	HatchedPVFPath string
	MinimumLevel   int
}

// PetHatchResolver resolves one real inventory egg item through the active
// runtime PVF. A nil resolver means that the pet mutation must fail closed.
type PetHatchResolver func(eggItemID int64) (PetHatchResolution, error)

// EnchantBeadResolution is the dependency-neutral runtime-PVF result passed
// from dnfbridge into the inventory domain for one enchant-by-bead attempt.
// CardItemID is the monster-card/enchant item the bead carries; zero means the
// bead template carries no enchant card and the request must be rejected as an
// invalid bead. UpgradeCounts is the card's allowed enchant-upgrade table; an
// empty table means only upgrade count zero is accepted.
type EnchantBeadResolution struct {
	RandomTitleEnchantConfigID uint16
	RandomTitleEnchantRowID    uint16
	CardItemID                 int64
	CardPVFPath                string
	TargetWhitelist            []int64
	AllowedEquipmentTypes      []string
	UpgradeCounts              []int64
	TargetEquipmentType        string
	TargetKind                 string
}

// EnchantBeadResolver resolves the bead and target templates through the
// active runtime PVF. A nil resolver means the enchant mutation must fail
// closed instead of trusting request or inventory-Extra metadata.
type EnchantBeadResolver func(beadItemID int64, targetItemID int64) (EnchantBeadResolution, error)

// AmplifyWeightedLevel is one runtime-PVF [amplification random value]
// level/weight pair carried by a specific Pure Gold material.
type AmplifyWeightedLevel struct {
	Level  byte
	Weight int64
}

// AmplifyItemResolution is the dependency-neutral runtime-PVF result used by
// class1/op204 (purify/clear) and class1/op205 (invest/twist/Pure Gold).
// Option values follow etc/amplifyitem.etc: 1..4 are the four concrete
// amplification attributes and 5 means the client-selected [all] family.
// InitialValues is keyed by the final client amplification attribute 1..4.
type AmplifyItemResolution struct {
	TargetKind            string
	TargetPVFPath         string
	TargetMinimumLevel    int64
	TargetRarity          int64
	EquipLevelConst       int64
	PurifyMaterialCount   int64
	ClearMaterialCount    int64
	InvestOption          byte
	InvestMaterialCount   int64
	ReinvestOption        byte
	ReinvestMaterialCount int64
	PureGoldOption        byte
	PureGoldMaterialCount int64
	InitialValues         map[byte]uint16
	PureGoldLevels        []AmplifyWeightedLevel
	MaterialPVFPath       string
}

// AmplifyItemResolver resolves the real material and target templates through
// the active runtime PVF. A nil resolver means the mutation must fail closed.
type AmplifyItemResolver func(materialItemID int64, targetItemID int64) (AmplifyItemResolution, error)

// RandomOptionWeightedQuantity is one runtime-PVF random-option count roll.
type RandomOptionWeightedQuantity struct {
	Quantity byte
	Weight   int64
}

// RandomOptionCandidate is one weighted, level-resolved magic-seal option.
// Type/Value1/Value2 map directly to the current NoPack raw item fields.
type RandomOptionCandidate struct {
	Type   byte
	Value1 byte
	Value2 byte
	Weight int64
}

// RandomOptionResolution is the complete runtime-PVF policy for one target.
// InitialGroups and ModifiedGroups contain the three equipment-specific option
// pools selected by optiongroupselection.etc.
type RandomOptionResolution struct {
	TargetKind           string
	TargetPVFPath        string
	TargetEquipmentKey   string
	TargetMinimumLevel   int64
	TargetRarity         int64
	Eligible             bool
	QuantityWeights      []RandomOptionWeightedQuantity
	InitialGroups        [][]RandomOptionCandidate
	ModifiedGroups       [][]RandomOptionCandidate
	BreakSealGoldCost    int64
	ModificationGoldCost int64
}

// RandomOptionResolver resolves the real target template through the active
// runtime PVF. Nil or incomplete results make the mutation fail closed.
type RandomOptionResolver func(targetItemID int64) (RandomOptionResolution, error)

// UpgradeTicketResolution is the dependency-neutral runtime-PVF result passed
// from dnfbridge into the inventory domain for one op50 ticket-scene upgrade
// attempt. TicketMode is "reinforce" or "amplify" when the material template
// carries [equipment (amplify )?reinforcement ticket]; an empty TicketMode
// means the material is not an upgrade ticket and the request must stay on
// the pending normal-reinforcement path. SuccessWeight is the ticket's
// success chance out of 100000 (percent*1000). TicketRandom marks the
// [enchant random] multi-candidate family, currently unsupported.
type UpgradeTicketResolution struct {
	TicketMode             string
	TicketRandom           bool
	TargetLevel            int64
	SuccessWeight          int64
	TargetUpgradeForbidden bool
	TargetEquipmentType    string
	TargetKind             string
	TicketPVFPath          string
	// Scene 是这一个消耗品的**场景**（厂商 `ItemUpgradeScene`）：
	//   ""/"ticket" = 强化券/增幅券档：成功直接跳到券写的 TargetLevel，失败永不摧毁；
	//   "portable"  = 一次性增幅器 / 凯丽的强化器档（`[action type] [portable …]`）：
	//                 消耗品**替代**表里的材料与金币，判定与惩罚档走同一张正常表
	//                 （所以失败**可以摧毁**，保护券在这一档同样生效）。
	// 现场（2026-09-28T18:36:26Z，角色 1）：「一次性增幅器」在材料槽里发出 op50，
	// 服务端没有 portable 档，于是按普通克伦特档校验 → error=174。见
	// inventory/upgrade_portable_consumable.go。
	Scene string
	// PortableLevelMin/Max 是消耗品允许的**当前强化/增幅等级**窗口
	// （`[action type]` 的第 1、2 个参数；厂商 `AllowsConsumableCurrentLevel`）。
	// 现场形态：一次性增幅器 `[portable amplify] 0 30`、凯丽的强化器 `[portable upgrade] 0 30`。
	PortableLevelMin int
	PortableLevelMax int
	// PortableMode 为 "reinforce"/"amplify" 时表示这一行是**便携强化/增幅消耗品**
	// （Scene == "portable"）：`[action type]` 逐字等于 `[portable upgrade]` /
	// `[portable amplify]`（含 `[new …]` 变体）。它与 TicketMode 互斥：
	// 券档里 TicketMode 非空、PortableMode 为空；便携档反之。
	//
	// 注意 `[portable genuine damage upgrade upgrede]`（便携式锻造炉）**不属于**这一族：
	// 那是锻造（separate upgrade）的路径，本实现逐字比对记号，不会误收。
	PortableMode string
	// PortableSuccessBonus 是 `[action type]` 的第 3 个参数，按**十万分比**的
	// **乘算**加成（厂商 `SuccessRateBonusWeight`：`weight * (100000+bonus)/100000`，
	// 即 30000 = 基础成功率 ×1.3）。唯一写在真档里的例子是「成功之兆强化器 / 增幅器」
	// （`[new portable upgrade] 0 14 30000`）。0 = PVF 没写第 3 个参数 = 不加成。
	PortableSuccessBonus       int
	MaterialUsableItemLevelSet bool
	// MaterialUsableItemLevelMin/Max 是材料槽那一件耗材的 `[check usable itemlevel]` 窗口
	// （目标装备的 `[minimum level]` 必须落在其中）。-1 = 该段缺席 = 不限目标等级。
	// 厂商 `ItemUpgradeConsumableResolver.FromPortable` 把这个窗口原样装进
	// `ItemUpgradeConsumableConfig.Restriction.ItemLevelMin/ItemLevelMax`（默认 -1），
	// `InventoryItemUpgradeService.ValidateRestrictions` 再用它判目标（回 ErrorRestriction 19）。
	MaterialUsableItemLevelMin int
	MaterialUsableItemLevelMax int
	// TargetMinimumLevel 是**目标装备** PVF 的 `[minimum level]`
	// （Tool/PvfLib/Models/EquipmentFile.cs:36/154，-1 = 该装备 PVF 没写这一段 = 未知）。
	// 厂商在 portable 档用它做等级窗口判定
	// （`EquipmentLevel = targetMetadata.MinimumLevel`，InventoryItemUpgradeService.cs:388/401
	// -> `AllowsItemLevel(context.EquipmentLevel)`，:486），不是 `[grade]`、也不是角色等级。
	TargetMinimumLevel int
}

// UpgradePortableConsumable 是"这次 op50 用的是哪一件便携强化/增幅消耗品"的
// 领域层事实（由 dnfbridge 的运行时 PVF 解析器给出）。它主要描述消耗品本身，另带一个
// 必须由目标装备侧提供的判据（UsableItemLevelMin/Max 与 TargetMinimumLevel）；
// 判定与消耗都在 inventory/owner.go 的 Upgrade 里完成。
type UpgradePortableConsumable struct {
	// ItemID 是消耗品的模板 id（材料槽那一行必须逐字等于它）。
	ItemID int64
	// Mode 是消耗品所属的模式（"reinforce"/"amplify"），必须与请求 mode 相同。
	Mode string
	// LevelMin/LevelMax 是允许的当前等级窗口（闭区间）。
	LevelMin int
	LevelMax int
	// SuccessBonus 是十万分比的乘算加成（0 = 无）。
	SuccessBonus int
	// PVFPath 是解析出来的 stackable 路径（只用于日志取证）。
	PVFPath            string
	UsableItemLevelSet bool
	// UsableItemLevelMin/Max 是这一件消耗品 `[check usable itemlevel]` 声明的目标装备等级
	// 窗口（真档一次性增幅器 10164868 = `1 90`）。-1 = 该段缺席 = 不限（保持原行为）。
	UsableItemLevelMin int
	UsableItemLevelMax int
	// TargetMinimumLevel 是**目标装备** PVF 的 `[minimum level]`，即厂商
	// `AllowsItemLevel(context.EquipmentLevel)` 的入参（不是 `[grade]`）。-1 = 未知/没写。
	TargetMinimumLevel int
}

// UpgradeTicketResolver resolves the material and target templates through
// the active runtime PVF. A nil resolver means the ticket mutation must fail
// closed instead of trusting request or inventory-Extra metadata.
type UpgradeTicketResolver func(materialItemID int64, targetItemID int64) (UpgradeTicketResolution, error)

// UpgradePolicyResolution carries the PVF-derived success/penalty parameters
// for one normal NPC upgrade attempt (non-ticket scene).
type UpgradePolicyResolution struct {
	SuccessWeight      int // out of 100000
	PenaltyType        int // 0/1=no change, 2=subtract PenaltyValue, 3=destroy
	PenaltyValue       int
	MaterialItemID     int
	MaterialCount      int
	DestroyBonusItemID int
	DestroyBonusCount  int
	// DestroyBonusIdentity is the destroy compensation item's runtime-PVF
	// identity (path / kind / stack type) plus its [stack limit]. The inventory
	// owner feeds it to dnfrepo.CanMergeItemStack so one compensation grant
	// merges into an existing same-identity row instead of always opening a new
	// slot, and caps the merge at the PVF limit (<= 0 = the PVF writes no such
	// section, which is the real shape of 强烈之痕迹 3243 / 浓缩的纯洁之骸 3244).
	DestroyBonusIdentity StackGrantIdentity
	// DestroyBonusContainer is the runtime-PVF container/segment the
	// compensation item belongs to (dnfbridge/currentPVFItemTargetContainer is
	// the single source of truth for that judgment). The inventory owner uses
	// it to (1) prefer same-identity rows inside that segment when merging and
	// (2) open the new slot inside that segment instead of the historical
	// quickbar/equipment order. 2026-09-28 live evidence: the amplify
	// compensation (item 3244, a `[material]`) was created at 0:12 - the
	// equipment page - while every other material lives in list 0 121..176.
	DestroyBonusContainer StackGrantContainer
	NoticeLevel           int // announcement threshold (-1 = disabled)
}

// StackGrantContainer 是"这次发放应该落在哪个容器的哪一段"。
//
// 它是 dnfbridge 侧唯一真源 `currentPVFItemTargetContainer` 的领域层投影：
// currentPVFItemTargetContainer 按运行时 PVF 的道具家族给出 list 与槽段
// （`[material]` → list 0 121..176、`[feed]`/`[creature]` → list 7 189..238 …），
// 领域层只按这个结果选格，不自己再判一次家族。
//
// 零值（SlotEnd < SlotStart）表示"没有容器信息"：调用方必须退回历史落点顺序，
// 绝不允许把零值当成"槽段 0..0"。
type StackGrantContainer struct {
	ContainerState *dnfrepo.CharacterContainerState
	ListType       byte
	SlotStart      int16
	SlotEnd        int16
}

// Valid 报告容器信息是否可用（零值不可用）。
func (c StackGrantContainer) Valid() bool {
	return c.SlotEnd >= c.SlotStart && c.SlotEnd > 0
}

// Contains 报告某个 (list, slot) 是否落在本容器段内。
func (c StackGrantContainer) Contains(listType byte, slot int16) bool {
	return c.Valid() && listType == c.ListType && slot >= c.SlotStart && slot <= c.SlotEnd
}

// StackGrantIdentity 是"这次发放的堆栈身份"（PVF 派生，与
// repository.ItemStackIdentityExtraKeys 同名同形）。空字段 = 该键缺省，
// 合并判据按"缺省即兼容"处理；它不参与实例身份的键（source / last_grant_* 等记账键）
// 不在这里出现。
type StackGrantIdentity struct {
	StackLimit    int64
	PVFPath       string
	ItemKind      string
	StackableType string
}

// Extra 把身份投影成 Extra 键，供 dnfrepo.CanMergeItemStack 的 incoming 一侧使用。
func (id StackGrantIdentity) Extra() map[string]string {
	extra := make(map[string]string, 3)
	if id.PVFPath != "" {
		extra["pvf_path"] = id.PVFPath
	}
	if id.ItemKind != "" {
		extra["item_kind"] = id.ItemKind
	}
	if id.StackableType != "" {
		extra["stackable_type"] = id.StackableType
	}
	return extra
}

// UpgradePolicyResolver resolves the PVF upgrade table row for the given mode
// and current upgrade level of the owned target template. Equipment rarity is
// resolved from PVF using targetItemID, never inventory metadata or packet text.
type UpgradePolicyResolver func(mode string, targetItemID int64, currentLevel int) (UpgradePolicyResolution, error)

// UpgradeProtectTicketResolution is the runtime-PVF identity of one inventory
// template asked about during the op50 destroy branch.
//
// Mode 为空表示"这个模板根本不是保护券"（绝大多数行都是这种），域层因此继续扫下一行。
// 非空表示它是该模式的保护券：Mode 与 op50 请求的 mode 同字面（"reinforce"/"amplify"）。
type UpgradeProtectTicketResolution struct {
	// Mode is "reinforce" or "amplify" when the stackable's [action type] is
	// that mode's protection ticket; "" for every other template.
	Mode string
	// RetainLevel is the level the equipment falls back to when this ticket
	// absorbs the destruction: the [action type] section's first integer, or 0
	// when the PVF writes no integer (the real shape of every live protection
	// ticket: `[action type] `[protect equipment]`` with no parameters).
	RetainLevel int
	// UnitCost is how many tickets one absorbed destruction consumes. The 86JP
	// reference consumes exactly one (`Cost = {MaterialCount: 1}`).
	UnitCost int64
	// PVFPath is the resolved stackable path, carried for the log line only.
	PVFPath string
}

// UpgradeProtectTicketResolver maps one inventory template id to its runtime-PVF
// protection-ticket identity. A nil resolver (or the legacy switch value) means
// the destroy branch keeps the pre-fix behavior: the equipment is destroyed.
type UpgradeProtectTicketResolver func(itemID int64) (UpgradeProtectTicketResolution, error)

// UpgradeRateBuffResolution is the runtime-PVF identity of one "upgrade success
// rate" buff potion (`[stackable type] [item upgrad buff usual upgrade]` /
// `[item upgrad buff amplify upgrade]`).
//
// Mode 为空表示这一行不是成功率 buff 药水（绝大多数 stackable 都是这种），域层因此
// 走既有路径（只消耗、不记账）。
type UpgradeRateBuffResolution struct {
	// Mode is "reinforce" or "amplify": which upgrade path this buff boosts.
	// The two families never cross (a 强化 buff never boosts an 增幅 roll).
	Mode string
	// BonusWeight is the PVF `[upgrade prob increase]` value in 1/100000 units.
	// 10000 = the rate is multiplied by 1.1. This is the vendor's own tag name
	// (PvfLib EquipmentFile.cs:182 `case "upgrade prob increase"`) and the
	// vendor's own multiplicative unit (ItemUpgradeModels.cs:271-273
	// "第3参数按十万分比额外加成保存，30000表示在基础成功率上额外加30%").
	BonusWeight int
	// DurationMillis is the PVF `[stat change duration]` first value in
	// milliseconds (live potions: 1800000 = 30 min, 600000 = 10 min,
	// 180000 = 3 min). <= 0 means the PVF writes no duration ⇒ the buff is not
	// recorded at all (fail closed: never invent a duration).
	DurationMillis int64
	// PVFPath is the resolved stackable path (log evidence only).
	PVFPath string
}

// UpgradeRateBuffResolver maps one op44 template id to its runtime-PVF
// success-rate buff identity. Nil (or the legacy switch value) keeps the
// pre-feature behavior: the potion is consumed and nothing is recorded.
type UpgradeRateBuffResolver func(itemID int64) (UpgradeRateBuffResolution, error)

// UpgradeProtectTicket reports what the destroy branch did about protection
// tickets on this attempt, so the bridge can emit one grep-able line: either
// "a ticket absorbed the destruction" (with the row, the item and the remaining
// count) or "no ticket was found, the equipment was destroyed". Nil for every
// op50 attempt that never reached the destroy branch.
type UpgradeProtectTicket struct {
	// Mode is the op50 mode literal ("reinforce" / "amplify") of this attempt.
	Mode string
	// Consumed is true when a matching ticket was found and one unit was
	// deducted, so the equipment survived at RetainLevel instead of being
	// destroyed.
	Consumed bool
	// Slot / ItemID / Remaining / PVFPath describe the consumed ticket row.
	// Zero when Consumed is false.
	Slot      int16
	ItemID    int64
	Remaining int64
	PVFPath   string
	// RetainLevel is the new equipment level when Consumed (the ticket's
	// [action type] parameter, clamped to the old level).
	RetainLevel int
	OldLevel    byte
	NewLevel    byte
	// Candidates counts the main-inventory rows the scan actually asked the
	// resolver about (evidence that the scan ran and how far it got).
	Candidates int
	// ResolverError is non-empty when at least one candidate row could not be
	// resolved through the runtime PVF; the first failure is reported verbatim.
	ResolverError string
	// Switch is the normalized DNFBRIDGE_UPGRADE_PROTECT_TICKET value in force.
	Switch string
}

// MagicBoxRewardEntry is one weighted candidate inside a reward group.
type MagicBoxRewardEntry struct {
	ItemID int64
	Weight int64
	Count  int64
}

// MagicBoxRewardGroup is one independently-drawn reward pool; each group
// yields DrawCount weighted picks per open (86JP RollBoosterRewards).
type MagicBoxRewardGroup struct {
	DrawCount int64
	Entries   []MagicBoxRewardEntry
}

// MagicBoxResolution is the dependency-neutral runtime-PVF result passed from
// dnfbridge into the package domain for one magic-box open. Kind is "random"
// for weighted pools ([random upgradable legacy] RANDOMBOX groups and the
// [booster]/[cera booster]/[booster random] booster-info family), "package"
// for grant-all [cera package], "unsupported" for client-selected families,
// and "" when the template is not an openable box. MaterialItemID is the
// per-open material requirement (0 when the box opens free); the box itself
// is never reported as a material.
type MagicBoxResolution struct {
	Kind                string
	Groups              []MagicBoxRewardGroup
	PackageItems        []MagicBoxRewardEntry
	MaterialItemID      int64
	MaterialCountPerUse int64
	BoxPVFPath          string
}

// MagicBoxResolver resolves one box template through the active runtime PVF.
// A nil resolver means the open must fail closed instead of trusting request
// or inventory-Extra metadata.
type MagicBoxResolver func(boxItemID int64) (MagicBoxResolution, error)

// MagicBoxRewardItem carries the grant metadata of one resolved reward
// template: inventory kind, stack limit (0 = unlimited), slot range, the seal
// flag derived from its PVF [attach type], its document path, and the absolute
// expiration resolved once from a relative PVF [usable period] at grant time.
//
// PremiumType/PremiumDurationSeconds are stamped only for runtime-PVF
// premiumlist_new.etc contract items. A contract reward is never placed in a
// slot: grant-time code must extend the account entitlement instead (see
// premium.ActivateContractGrant), exactly like the 86JP/A21 reference
// (InventoryRewardGrantService.TryPlanPremium runs before insert planning).
type MagicBoxRewardItem struct {
	ItemID           int64
	Kind             string
	TargetListType   byte
	EquipmentType    string
	StackLimit       int64
	SlotStart        int16
	SlotEnd          int16
	Seal             bool
	PVFPath          string
	ExpireAt         time.Time
	UsablePeriodDays int64
	Durability       uint16
	AllowQuickSlots  bool
	PreferQuickSlots bool
	// PremiumType/PremiumDurationSeconds come from etc/premiumlist_new.etc
	// [type]/[item]/[term]; zero means the reward is an ordinary item.
	PremiumType            int64
	PremiumDurationSeconds int64
}

// MagicBoxRewardItemResolver resolves one reward template through the active
// runtime PVF for the durable grant step.
type MagicBoxRewardItemResolver func(itemID int64) (MagicBoxRewardItem, error)

// RandomRewardItemOutcome is one weighted [chn random image percent] result.
// A zero Reward.ItemID is the PVF's no-item visual-effect outcome.
type RandomRewardItemOutcome struct {
	Weight      int64
	Reward      MagicBoxRewardItem
	EffectIndex byte
}

// RandomRewardItemResolution is the runtime-PVF authorization for a normal
// op44 consumable with [stackable type] [random reward item]. The source is
// always consumed; one weighted outcome may add one repository-backed item.
type RandomRewardItemResolution struct {
	SourceItemID  int64
	SourcePVFPath string
	StackableType string
	Outcomes      []RandomRewardItemOutcome
}

// RandomRewardItemResolver keeps the random-reward table and reward template
// metadata in dnfbridge, rather than trusting mutable inventory Extra fields.
// A zero SourceItemID means the candidate is not this consumable family.
type RandomRewardItemResolver func(itemID int64) (RandomRewardItemResolution, error)

// PremiumContractResolution is the dependency-neutral runtime-PVF result for
// one contract item activation: the premium type and its duration in seconds,
// read from premiumlist_new.etc [item]/[term] entries only (its [target item]
// metadata is never an activation key).
type PremiumContractResolution struct {
	ItemID          int64
	PremiumType     int64
	DurationSeconds int64
}

// PremiumContractResolver resolves one candidate item through the active
// runtime PVF. A zero PremiumType means the item is not a contract item and
// the caller must fall back to the ordinary use-stackable path.
type PremiumContractResolver func(itemID int64) (PremiumContractResolution, error)

// StackableUseProvenanceResolution is the dependency-neutral runtime-PVF
// identity of one op44/op251 source item: the item's path in the active
// archive plus its `[stackable type]` line, read from the .stk document itself.
//
// Found=false means the runtime PVF has no answer for this item id (unknown id,
// not a stackable list entry, or the archive is unavailable); the caller must
// then fall back to the stored inventory metadata, which is the pre-existing
// contract. This is deliberately the *identity*, not a verdict: the family
// accept set stays in the inventory domain (inventory/use_upgrad_buff.go).
type StackableUseProvenanceResolution struct {
	ItemID        int64
	PVFPath       string
	StackableType string
	ActionType    string
	Found         bool
}

// StackableUseProvenanceResolver resolves one candidate op44/op251 source item
// through the active runtime PVF (stackable/stackable.lst -> the item's .stk),
// rather than trusting the mutable inventory Extra["stackable_type"] column.
// An error means "no runtime-PVF answer", never "reject the item".
type StackableUseProvenanceResolver func(itemID int64) (StackableUseProvenanceResolution, error)

// DamageFontExpirationMode describes the expiration contract carried by a
// runtime-PVF [add damage font skin] item.
type DamageFontExpirationMode byte

const (
	DamageFontExpirationUnknown DamageFontExpirationMode = iota
	DamageFontExpirationPeriod
	DamageFontExpirationFixed
	DamageFontExpirationUnlimited
)

// DamageFontResolution is the runtime-PVF authority for one damage-font item.
// A zero FontIndex or invalid resolution must not consume the source item.
type DamageFontResolution struct {
	Valid           bool
	PVFPath         string
	ActionType      string
	FontIndex       uint16
	ExpirationMode  DamageFontExpirationMode
	PeriodDays      int64
	FixedExpiration time.Time
}

// DamageFontResolver resolves one inventory source through the active PVF.
type DamageFontResolver func(itemID int64) (DamageFontResolution, error)

// RepairCostEvidence is the dependency-neutral runtime-PVF repair evidence
// for one equipment item, matching the 86JP EquipmentRepairPriceProvider
// inputs. MaxDurability <= 0 means the item is not repairable. The rates come
// from equipment/pricetable.tbl ([repair cost], [quick repair cost rate]) and
// etc/upgrade.etc [repair cost rate by upgrade level]; EquipmentType is the
// normalized PVF [equipment type] token used for the repair-all eligibility
// filter.
type RepairCostEvidence struct {
	EquipmentType   string
	MaxDurability   int64
	RepairPrice     int64
	Grade           int64
	RepairCostRate  float64
	QuickRepairRate float64
	UpgradeRates    []float64
}

// RepairCostResolver resolves one candidate equipment item through the active
// runtime PVF. A nil resolver means the repair cost mutation must fail closed
// instead of trusting request or inventory-Extra metadata.
type RepairCostResolver func(itemID int64) (RepairCostEvidence, error)

// MailboxItemResolution is the current-PVF evidence used by the exact
// NoPack mailbox flow. Price is the item's [price] value; Kind and
// EquipmentType preserve the PVF type needed to frame a mailbox attachment
// without treating mutable inventory Extra data as the type authority.
// SlotStart/SlotEnd and StackLimit are used when an attachment is claimed:
// they put the item in its real main-inventory tab rather than treating the
// op13 main-list expansion marker as a physical slot count.
type MailboxItemResolution struct {
	Price         int64
	PVFPath       string
	Kind          string
	EquipmentType string
	SlotStart     int16
	SlotEnd       int16
	StackLimit    int64
	// PreferQuickSlot 由 PVF 解析器与购买、拾取共用规则给出；可堆叠本身不代表
	// 优先在快捷栏新开格；普通装备/材料的满页回落资格通过 AllowQuickSlots 给出。
	PreferQuickSlot bool
	AllowQuickSlots bool
	// TargetListType 是领取附件时应该写入的容器；零值即主背包
	// (dnfrepo.MainInventoryListType)。宠物更名卡等 [creature]/[feed] 道具只在
	// 宠物背包 list 7 里有合法槽位，放进主背包客户端既看不到也用不了。
	TargetListType byte
	// AvatarSocketData 是装扮栏(list 1)条目的创建期徽章孔向量（皮肤/光环按
	// 运行时 PVF 声明天生带孔）。当前客户端把它放在固定 0x77 行之后的可选 blob
	// 里——list 1 每行两个 u32 长度前缀的 blob，第一个就是孔位——所以它必须落到
	// 堆栈 Extra 的 avatar_socket_data 上，烘进固定行字节客户端不会读。
	AvatarSocketData []byte
	// ExpireAt / DefaultExtra 是附件自身没有实例到期时间时，按运行时 PVF 补上的
	// 到期投影（当前客户端把"到期时间为 0"渲染成"已超过使用期限"）。DefaultExtra
	// 是 dnfbridge 用同一条已测的投影算出来的创建期 Extra，只在附件缺该键时合并。
	ExpireAt     time.Time
	DefaultExtra map[string]string
	// UsablePeriodDays is the relative period declared by the item template.
	// Mail claims resolve it from the original attachment/mail, never claim time.
	UsablePeriodDays int64
	// OriginalExpiry reads only the attachment's existing instance evidence.
	// It does not use the current time or generate a new grant deadline.
	OriginalExpiry func(dnfrepo.MailAttachment) time.Time
	// ProjectAttachment resolves evidence that belongs to one durable
	// attachment rather than to the item template. The second argument is the
	// trusted mail creation anchor, or the zero time when the caller cannot
	// establish one. Existing instance evidence must win over any projection.
	ProjectAttachment MailboxAttachmentProjector
	// EpicPieceID 非零表示这件附件是**史诗碎片**（`etc/epicpieceinfo.etc`
	// `[piece list]` 的成员）：它必须进账号级史诗图鉴账本，**不写任何背包行**。
	// 只有 dnfbridge 同时提供了 Request.EpicPieceCreditor 时才允许置位；为零
	// （含开关回退档）时 mail 模块走原来的入包路径，逐字节不变。
	EpicPieceID uint32
	// AccountWarehouseSlot 非零表示这件附件按"小晶块/灵魂一律进账号仓库"落到
	// 账号级固定格（354..365），**不写任何背包行**。判据是
	// dnfrepo.AccountWarehouseSlotForItem（唯一映射）；只有 dnfbridge 同时提供了
	// Request.WarehouseCreditor 时才允许置位；为零（含开关回退档）时 mail 模块走
	// 原来的入包路径，逐字节不变。
	AccountWarehouseSlot int16
}

// MailboxAttachmentProjection is per-attachment evidence derived from a
// trusted anchor. Zero ExpireAt means the resolver has no projection; an empty
// Extra map means it contributes no instance keys.
type MailboxAttachmentProjection struct {
	ExpireAt time.Time
	Extra    map[string]string
}

// MailboxAttachmentProjector receives the attachment and the mail creation
// anchor. It must not substitute claim time for a missing anchor.
type MailboxAttachmentProjector func(dnfrepo.MailAttachment, time.Time) (MailboxAttachmentProjection, error)

// MailboxItemResolver resolves each durable attachment template through the
// active runtime PVF. A nil resolver makes attachment sends fail closed.
type MailboxItemResolver func(itemID uint32) (MailboxItemResolution, error)

// MailboxEpicPieceCredit 是一条已经计入账号账本的史诗碎片增量结果。
// Balance 是入账**之后**的绝对数量：0x047D 的增量包写的就是绝对值
// （见 dnfbridge/epic_piece_book.go 的 buildCurrentEpicPieceBalanceSyncBody）。
type MailboxEpicPieceCredit struct {
	PieceID uint32
	Count   int64
	Balance uint32
}

// MailboxEpicPieceCreditor 由 dnfbridge 提供，在 mail 模块的领取事务内部把一件
// 碎片计入账号级账本并返回入账后的绝对值。它刻意是函数而不是仓储接口：
// 账本落在 `dnf_account_metadata`（账号级、跨角色共享），不属于邮件资产事务的
// 三个仓储（characters / inventories / mailboxes），所以只能由桥接层注入。
type MailboxEpicPieceCreditor func(ctx context.Context, pieceID uint32, count int64) (uint32, error)

// MailboxWarehouseCredit 是一条已经落到账号晶块仓库的结果。Balance 是入账之后
// 该固定格里的**绝对数量**（与 op14 行写的最终堆叠量同义）。
type MailboxWarehouseCredit struct {
	Slot    int16
	ItemID  uint32
	Count   int64
	Balance int64
}

// MailboxWarehouseCreditor 由 dnfbridge 提供，在 mail 模块的领取流程里把一件
// 小晶块/灵魂并入账号级晶块仓库的固定格并返回入账后的绝对数量。
//
// 它刻意是函数而不是仓储接口，理由与 MailboxEpicPieceCreditor 相同：账号晶块仓库
// 落在 `dnf_account_inventory_items`（账号级、跨角色共享），不属于邮件资产事务的
// 三个仓储（characters / inventories / mailboxes），只能由桥接层注入。
//
// extra 是这条堆栈要写进 Extra 的身份证据（item_kind / pvf_path / stackable_type），
// 由 mail 模块按同一条领取投影给出，桥接层不得另造。
type MailboxWarehouseCreditor func(ctx context.Context, slot int16, itemID uint32, count int64, extra map[string]string) (int64, error)

// PartyState 保存当前已经验证的单人队伍会话字段。
// 它只属于 game 连接临时态，真正多人队伍 owner 接入前不做持久化。
type PartyState struct {
	PartyID          int
	IsLeader         bool
	UserID           uint16
	UserState        byte
	Members          []PartyMemberState
	RequestPrefix0   byte
	RequestPrefix1   byte
	NameBytes        []byte
	MemberSelectCode byte
	MaxMembers       byte
	SelectionID      uint32
	SelectionCode    byte
	SelectionValue   uint16
	RecruitFlag      byte
	TargetMode       byte
	TargetDungeonID  uint16
	ReserveLeaveFlag byte
}

// PartyMemberState 是已验证队伍包里能安全写出的队员槽位状态。
// Slot 是 RuntimePartyMember.Slot 的投影：0 是合法槽位；多个成员同时为 0
// 表示旧调用方没有提供固定槽位，bridge 会按输入顺序补成 0..n-1。
type PartyMemberState struct {
	UserID    uint16
	UserState byte
	HPPercent byte
	MPPercent byte
	Slot      byte
}

// Result 表示模块化分流后的处理结果。
// ResponseAllowed=false 表示证据不足或被阻断，bridge 不能伪造成功 ACK。
// EquipmentMoveFact is returned only after an op19 movement owner succeeds.
// It carries domain outcome, independent of notification delivery or log text.
type EquipmentMoveFact struct {
	Changed       bool
	WornClaimNoop bool
}

type Result struct {
	EquipmentMoveFact *EquipmentMoveFact
	Decision          Decision
	Handled           bool
	ResponseAllowed   bool
	Operation         string
	Reason            string
	UpperResponses    []UpperResponse
	// 穿戴装备原位修改后，在结果前按槽发送仓储权威op14，更新既有物品对象。
	// 仅供附魔/精炼；装备移动继续使用自身的生命周期边界。
	WornEquipmentSlotRefreshes []int16
	// A committed wear move without actor appearance refresh still relocates
	// registered Buff items. Reproject only after the move ACK and item rows;
	// appearance-bearing moves already restore Buff at their actor boundary.
	RefreshSelectedBuffRegistration bool
	// 消耗凭证仅由成功返回的库存事务提供，bridge 在回包前关联本局拾取记录。
	StackableConsumption *StackableConsumption
	// MailboxAlarmRecipientID asks the bridge to deliver the exact class0/0x63
	// mailbox alarm to this already-online character after the sender's ACK and
	// its local post-actions are fully written. Zero means no cross-session send.
	MailboxAlarmRecipientID uint16
	// ItemSlotRefreshes names only the durable item rows changed by this
	// command. The bridge reloads and projects these slots through the current
	// EXE's incremental item-update protocol after every ACK succeeds.
	//
	// List 7 (the pet container) is the one documented exception: the bridge
	// deliberately turns a list-7 entry here into a deferred whole-container
	// send, because a list-7 op14 row that has to create its object crashes the
	// current EXE (see dnfbridge/current_item_list_send.go). A pet equipment
	// move must therefore NOT use this field for its list-7 slot; it uses
	// PetMoveSlotRefreshes instead.
	ItemSlotRefreshes []ItemSlotRefresh
	PostActions       []PostAction
	// PetMoveSlotRefreshes names the list-7 slots whose row this command
	// changed, for pet/artifact equipment moves only (op19 with a list-3
	// slot 26..29 endpoint). It is the exact analogue of the vendor's
	// InventoryMoveResult.PetCreatureRefreshSlots
	// (Game/Inventory/InventoryModels.cs:59), filled from the same durable
	// changed-slot set that the vendor collects in
	// Network/Handlers/InventoryHandler.Move.cs:184-195 and projects with
	// InventoryRefreshSender.SendUpdateItemList(InventoryListType.Pet, slots).
	// The bridge decides with DNFBRIDGE_PET_MOVE_CONTAINER_REFRESH whether it
	// sends these rows or the legacy five-container replay; see
	// dnfbridge/equip_move_refresh.go.
	PetMoveSlotRefreshes []ItemSlotRefresh
	// PremiumContractActivations lists contract entitlements this command added
	// to the account directly. Contract items never own an inventory row and are
	// never mailed, so the bridge must announce the activation itself (current
	// EXE class0 premium-activated notice; op898 for the crystal contract) after
	// the ACK. Empty for every command that grants no contract.
	PremiumContractActivations []PremiumContractActivation
	// 仅投影已经提交的账号幸运值；不参与扣道具或奖励结算。
	SeriaLuck *SeriaLuckProjection
	// EpicPieceCredits lists the account-level epic-piece ledger increments this
	// command already committed. Fragments own no inventory row and are never
	// mailed, so the bridge must announce each increment itself through the
	// class0/0x047D ledger notice after the ACK — exactly like a contract
	// activation, and instead of an op14 row refresh. Empty for every command
	// that grants no fragment.
	EpicPieceCredits     []MailboxEpicPieceCredit
	AccountWalletChanged bool
	// UpgradeDestroyBonusMerge describes where the "equipment destroyed by a
	// failed reinforcement/amplification" compensation item landed: which row
	// received it, whether that row was an existing same-identity stack or a
	// freshly opened slot, and which placement switch decided it. Nil for every
	// command that is not the op50 destroy branch. The bridge owns the
	// grep-able event (dnfbridge/game_aligned.go) because the inventory domain
	// has no logger.
	UpgradeDestroyBonusMerge *UpgradeDestroyBonusMerge
	// UpgradeProtectTicket describes the protection-ticket decision of the op50
	// destroy branch (consumed one → the equipment survived; none found → it was
	// destroyed). Nil for every command that never reached that branch, and for
	// the legacy switch value. The bridge owns the grep-able event.
	UpgradeProtectTicket *UpgradeProtectTicket
	// Forging 是 op438 锻造这一次的领域结果（含校验类拒绝）。dnfbridge 用它
	// 组装 class1 438 回包：成功/掷点失败回 13 字节体，校验类拒绝回 0+u8 error。
	// 见 dnfbridge/forging_codec.go。
	Forging *ForgingOutcome
	// AuthoritativeListRefreshes names container list types whose class0/0x000D
	// refresh inside UpperResponses must be rebuilt from the bridge's session-scoped
	// authoritative projection (the same builder login bootstrap, the container-list
	// post actions and the cera shop use) instead of the module-provided repository
	// snapshot.
	//
	// op13 is whole-table semantics: the client replaces the whole container, including
	// rows the module snapshot does not own. For list 0 those are the reserved 0:0
	// wallet row (gold lives in Character.Stats), the account-shared 0:354..0:365
	// crystal/soul warehouse rows (AccountInventory), and the per-row fields the
	// shared 0x77 encoder does not reproduce (equipment quality seed at raw+0x06,
	// emblem/socket vector, effect rune, stackable use period, expiration). Shipping
	// the snapshot instead erases or rewrites them in the client while the database
	// stays intact (2026-10-14 role-106 op272 evidence).
	//
	// The bridge sends the rebuild in the same position and envelope as the response it
	// replaces, so the refresh still precedes the class1 ACK of the same Result. It
	// only ever looks at (classification 0, msg 0x000D) responses whose list type is
	// named here, so every other opcode keeps its bytes. When the authoritative build
	// fails the bridge drops that one refresh (it never falls back to the snapshot
	// body) and still sends the remaining responses, because the durable mutation and
	// its ACK must not be lost.
	//
	// Empty = previous behavior: every UpperResponse body is sent verbatim.
	AuthoritativeListRefreshes []byte
}

type SeriaLuckProjection struct {
	Before int64
	After  int64
}

type StackableConsumption struct {
	CharacterID    string
	ListType       byte
	SlotIndex      int16
	ItemID         int64
	PreviousCount  int64
	RemainingCount int64
}

// UpgradeDestroyBonusMerge is the destroy-branch compensation placement,
// reported so the bridge can emit one durable, grep-able line naming the item,
// the granted amount, the chosen slot, whether it merged or created a slot, and
// the switch value that was in force.
type UpgradeDestroyBonusMerge struct {
	ItemID        int64
	GrantedCount  int64
	PlacedCount   int64
	Slot          int16
	Slots         []int16
	Merged        bool
	Created       bool
	Identity      StackGrantIdentity
	Container     StackGrantContainer
	PlacementMode string
}

// PremiumContractActivation is one account-level contract extension produced by
// a grant command. ExpireAt is the absolute unix expiry after the extension, so
// the bridge derives the remaining seconds with the same clock it uses for
// every other premium projection.
type PremiumContractActivation struct {
	ItemID      int64
	PremiumType int64
	Units       int64
	ExpireAt    int64
}

type ItemSlotRefresh struct {
	ListType  byte
	SlotIndex int16
}

// PostAction describes a bridge-owned state refresh that must run only after
// every durable owner mutation and protocol ACK in UpperResponses succeeds.
// Business modules select the action; the bridge remains responsible for the
// current EXE-specific packet body and session context.
type PostAction string

const (
	PostActionRefreshSelectedActorAppearance        PostAction = "refresh_selected_actor_appearance"
	PostActionRefreshSelectedActorExpandedEquipment PostAction = "refresh_selected_actor_expanded_equipment"
	PostActionRefreshSelectedItemContainers         PostAction = "refresh_selected_item_containers"
	// Guild medal moves only invalidate list38, not every character container.
	PostActionRefreshGuildMedalContainer PostAction = "refresh_guild_medal_container"
	// 穿脱装备/时装（op19）专用的整容器重发动作。它与
	// PostActionRefreshSelectedItemContainers 的旧口径逐字节等价（class0/op13
	// list 0/1/2/7/38 五个整容器），但**只由穿脱路径发出**，因此 dnfbridge 可以
	// 单独按开关取消它而不影响盒子/仓库/宠物等其它路径。
	// 默认档不再重发（与厂商 9.16 的 item_slot_refresh_count=0 同口径），
	// 证据与一键回退见 dnfbridge/equip_move_refresh.go。
	PostActionRefreshSelectedWearContainers PostAction = "refresh_selected_wear_containers"
	// 宠物/神器穿戴（op19 落点 list 3 槽 26..29）专用的回包后刷新动作。
	// 它与 PostActionRefreshSelectedItemContainers 的旧口径逐字节等价
	// （class0/op13 list 0/1/2/7/38 五个整容器，且**从不包含落点 list 3**），
	// 但**只由宠物/神器搬运路径发出**，因此 dnfbridge 可以单独按开关把它换成
	// 厂商 9.16 的逐槽口径（见 Network/Handlers/Pets/PetInventoryMoveCoordinator.cs:58-81）
	// 而不影响盒子/结算/仓库等其它路径。证据与一键回退见
	// dnfbridge/equip_move_refresh.go。
	PostActionRefreshSelectedPetMoveSlots PostAction = "refresh_selected_pet_move_slots"
	// Equipment slot item rows are list 3.  They carry mutable equipped-item
	// instance state (for example socket/emblem tail bytes) that mode0
	// appearance refreshes do not reliably update.
	PostActionRefreshSelectedEquipmentSlots PostAction = "refresh_selected_equipment_slots"
	// Account cargo is list 12.  It must be rebuilt by dnfbridge from the
	// authenticated account record and account inventory, never by replaying a
	// captured cargo body or reading the selected character's settings.
	PostActionRefreshSelectedAccountCargo PostAction = "refresh_selected_account_cargo"
	PostActionRefreshSelectedActorSkills  PostAction = "refresh_selected_actor_skills"
	// Creature state is the current scene class0/op105 raw table. The bridge
	// sends it only after the selected actor is scene-ready; it is not part of
	// the class1/op19 success body.
	PostActionRefreshSelectedCreatureState PostAction = "refresh_selected_creature_state"
	// Crystal-contract state is current NoPack class1/op898. It must follow a
	// durable type-97 activation so the native client can bind its persisted
	// cube selection to the newly active contract immediately.
	PostActionRefreshCrystalContractState PostAction = "refresh_crystal_contract_state"
	PostActionRefreshFatigueState         PostAction = "refresh_fatigue_state"
	// Damage-font ownership is a character-stat projection consumed by NOTI
	// 1239. It follows the class1 op515 ACK and source-slot refresh.
	PostActionRefreshSelectedDamageFontState PostAction = "refresh_selected_damage_font_state"
	PostActionDamageFontExpiredNotice        PostAction = "damage_font_expired_notice"
	// Party-frame state is carried inside the current NoPack class0/op9 kind0
	// actor record. The bridge rebuilds that record from the selected actor and
	// live party slots before op153/op7 update the visible frame.
	PostActionRefreshSelectedPartyFrame PostAction = "refresh_selected_party_frame"
)

// UpperResponse 描述已经有证据允许 dnfbridge 发出的 raw upper 响应。
// Body 必须是完整业务包体；如果包体已经包含成功字节，调用方不能再套 upperSuccessBody。
type UpperResponse struct {
	MsgID          uint16
	Body           []byte
	Classification byte
	AllowCodec     bool
}

// Handler 是单个 DNF 业务域的命令处理入口。
type Handler interface {
	Domain() dnfenum.AlignedDomain
	Handle(context.Context, Request) (Result, error)
}

// Registry 按业务域保存 handler，避免 dnfbridge 直接堆积各玩法 switch。
type Registry struct {
	handlers map[dnfenum.AlignedDomain]Handler
}

// NewRegistry 创建模块化命令注册表。
func NewRegistry(handlers ...Handler) Registry {
	registry := Registry{handlers: make(map[dnfenum.AlignedDomain]Handler)}
	for _, handler := range handlers {
		if handler == nil {
			continue
		}
		registry.handlers[handler.Domain()] = handler
	}
	return registry
}

// DefaultRegistry 返回当前可用的模块化注册表。
// 具体业务 handler 尚未补齐时保持空表，只做证据化 pending/blocked 分流。
func DefaultRegistry() Registry {
	return NewRegistry()
}

// Route 根据 EXE 证据分类并交给对应业务域 handler。
func (r Registry) Route(ctx context.Context, req Request) (Result, bool, error) {
	// A numeric type is only a CmdPacket inside the current EXE command
	// dispatcher. Other command classes have independent registration tables.
	if req.CommandKnown && dnfenum.GameCmd(req.Command) != dnfenum.GameCmdCommand {
		return Result{}, false, nil
	}
	decision, ok := Classify(req.Opcode)
	if !ok {
		return Result{}, false, nil
	}
	if decision.Action == ActionBlocked {
		return Result{
			Decision:        decision,
			Handled:         true,
			ResponseAllowed: false,
			Operation:       string(decision.Action),
			Reason:          decision.Reason,
		}, true, nil
	}
	handler := r.handlers[decision.Domain]
	if handler == nil {
		return Result{
			Decision:        decision,
			Handled:         true,
			ResponseAllowed: false,
			Operation:       "module_unregistered",
			Reason:          "模块 handler 尚未注册",
		}, true, nil
	}
	req.Body = append([]byte(nil), req.Body...)
	result, err := handler.Handle(ctx, req)
	if result.Decision == (Decision{}) {
		result.Decision = decision
	}
	for i := range result.UpperResponses {
		result.UpperResponses[i].Body = append([]byte(nil), result.UpperResponses[i].Body...)
	}
	result.PostActions = append([]PostAction(nil), result.PostActions...)
	result.AuthoritativeListRefreshes = append([]byte(nil), result.AuthoritativeListRefreshes...)
	if result.StackableConsumption != nil {
		fact := *result.StackableConsumption
		result.StackableConsumption = &fact
	}
	return result, true, err
}
