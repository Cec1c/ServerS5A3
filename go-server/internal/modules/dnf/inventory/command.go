// 本文件定义背包类命令计划。
// 计划只承载已解析的客户端意图；真正资产变更必须进入可靠 owner、事务和幂等边界后才能回包。
package inventory

import (
	"fmt"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfplacement "longheng.io/server/internal/modules/dnf/itemplacement"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Command 是 inventory handler 交给后续 inventory/equipment/wallet owner 的最小命令计划。
type Command struct {
	ItemPlacement          func(dnfrepo.ItemStack) (dnfplacement.SlotPolicy, error)
	CargoInsert            func(dnfrepo.ItemStack, byte) error
	Operation              string
	AccountID              string
	SelectedCharacterID    uint16
	SourceListType         byte
	SourceSlotIndex        int16
	SourceInstanceValue    int32
	ReservedValue          uint32
	MoveCount              int32
	DestinationListType    byte
	DestinationSlotIndex   int16
	DestinationInstance    int32
	DestinationStack       int32
	ActorIndex             int32
	TrailingState0         byte
	TrailingState1         byte
	EntryCount             int
	DeleteEntries          []DeleteEntry
	Count                  int32
	ItemTemplateID         int32
	RepairSlot             int16
	AutoRepair             bool
	QuickRepair            bool
	TargetSlotIndex        int16
	TargetItemTemplateID   int32
	MaterialSlotIndex      int16
	MaterialItemTemplateID int32
	OptionalTicketSlot     int16
	Mode                   string
	TargetItemName         string
	AmplifyAction          byte
	SelectedAmplifyOption  byte
	RandomOptionIndex      byte
	InventoryManagerState  uint16
	BeadListType           byte
	BeadSlotIndex          int16
	TargetListType         byte
	ItemSpace              byte
	DisjointItemSlotIndex  int16
	ContextValue           int32
	ActionIndex            uint32
	DamageFontIndex        uint16
	Category               byte
	Condition              byte
	NeedsOwner             string
	// SortLock / SortLockRequested 承接 op713/714 的位置锁意图（DOC-067）：
	// SortLock 由 opcode 决定（713=锁、714=解锁），不是 toggle。
	SortLock          bool
	SortLockRequested bool
	// Upgrade policy fields (populated by dnfbridge from PVF table).
	// SuccessWeight is out of 100000; >= 100000 means guaranteed success.
	UpgradeSuccessWeight int
	// PenaltyType: 0=fail no change, 1=downgrade, 3=destroy.
	UpgradePenaltyType int
	// MaterialItemID/Count are the runtime-PVF upgrade table requirement.
	UpgradeMaterialItemID  int
	UpgradeMaterialCount   int
	UpgradeCostResolver    alignedcmd.UpgradeCostResolver
	UpgradeNPCPresent      bool
	UpgradeNPCID           uint16
	UpgradeNPCMaterialSlot int16
	// DestroyBonusItemID/Count: compensation item on destruction.
	UpgradeDestroyBonusItemID int
	UpgradeDestroyBonusCount  int
	// UpgradeDestroyBonusIdentity 是补偿道具在运行时 PVF 里的身份（pvf_path /
	// item_kind / stackable_type）与 [stack limit]（<= 0 = PVF 没写这一段 = 无上限，
	// 与强烈之痕迹 3243 / 浓缩的纯洁之骸 3244 的真实形状一致）。身份用于"能不能并进
	// 既有行"，上限用于"并多少"；两者都是道具定义派生的值，不是实例身份。
	UpgradeDestroyBonusIdentity alignedcmd.StackGrantIdentity
	// UpgradeDestroyBonusContainer 是该道具在运行时 PVF 里的落位容器/槽段
	// （dnfbridge 的 currentPVFItemTargetContainer 是唯一真源）：合并时段内行优先，
	// 新开格落在段内。零值 = 没有容器信息 ⇒ 退回历史落点顺序。
	UpgradeDestroyBonusContainer alignedcmd.StackGrantContainer
	// NoticeLevel: announcement threshold (-1 = disabled).
	UpgradeNoticeLevel int
	// UpgradePolicyError records PVF resolver failures so packet-log command
	// reasons do not silently degrade into materialPVF=(0,0).
	UpgradePolicyError string
	// UpgradeProtectTicketResolver 认出背包里的装备强化/增幅保护券（运行时 PVF 的
	// `[action type]`）。为零 = 不做保护券判定（改动前的行为，也是所有既有测试的
	// 行为）。见 upgrade_protect_ticket.go。
	UpgradeProtectTicketResolver alignedcmd.UpgradeProtectTicketResolver
	// UpgradePortable 非空表示这次 op50 的材料槽里是**便携强化/增幅消耗品**
	// （一次性增幅器 / 凯丽的强化器）：它替代表里的材料与金币，成功率和惩罚档走
	// 同一张正常表（失败可摧毁，保护券照样生效）。为零 = 克伦特档（既有行为）。
	// 由 handler 从 UpgradeTicket 的解析结果搬过来。见 upgrade_portable_consumable.go。
	UpgradePortable *alignedcmd.UpgradePortableConsumable
	// UseUpgradeBuff 非空表示这次 op44 用的是「强化/增幅成功率 buff 药水」：
	// 扣掉药水之后要把 buff 记进角色 Stats（同一个角色资产事务），op50 掷点时再读。
	// 为零 = 普通消耗品（既有行为）。见 upgrade_rate_buff.go。
	UseUpgradeBuff *alignedcmd.UpgradeRateBuffResolution
	// StackableUseProvenanceResolver 是 op44/op251 的**运行时 PVF 身份**解析器，
	// 由 alignedcmd.Request 透传（见 use_upgrad_buff.go 的"PVF 优先、元数据兜底"判据）。
	// 为零 = 只用库存元数据判定 = 改动前的行为（所有既有调用方与测试无需改动）。
	StackableUseProvenanceResolver alignedcmd.StackableUseProvenanceResolver
	// ForgingConfirmation 是 op438 请求尾字节。当前 EXE 把它当发送函数参数传入，
	// 取值域没有证据（见 Docs/D33_FORGING_20261014.md），因此只做日志、不参与校验。
	ForgingConfirmation byte
}

// NewDeleteCommand 构造删除物品命令计划。
func NewDeleteCommand(req alignedcmd.Request, parsed DeleteRequest) Command {
	cmd := baseCommand(req, "delete_item", "inventory owner + mutation id + backpack/USERINFO/NOTI order")
	cmd.SourceListType = parsed.ListType
	cmd.SourceSlotIndex = parsed.SlotIndex
	cmd.Count = int32(parsed.Count)
	cmd.EntryCount = len(parsed.Entries)
	cmd.DeleteEntries = append([]DeleteEntry(nil), parsed.Entries...)
	if parsed.Extended && len(parsed.Entries) > 0 {
		cmd.SourceSlotIndex = parsed.Entries[0].SlotIndex
		cmd.ItemTemplateID = parsed.Entries[0].ItemID
		cmd.Count = parsed.Entries[0].DeleteCount
	}
	return cmd
}

// NewMoveCommand 构造移动或拆分物品命令计划。
func NewMoveCommand(req alignedcmd.Request, parsed MoveItemspaceRequest) Command {
	cmd := baseCommand(req, "move_itemspace", "inventory/equipment/cargo owner + mutation id + backpack/USERINFO/NOTI order")
	cmd.CargoInsert = req.InventoryCargoInsert
	cmd.SourceListType = parsed.SourceListType
	cmd.SourceSlotIndex = parsed.SourceSlotIndex
	cmd.SourceInstanceValue = parsed.SourceInstanceValue
	cmd.MoveCount = parsed.MoveCount
	cmd.DestinationListType = parsed.DestinationListType
	// 本次PVF分类校验用于金库存取；非金库移动保持原分类守卫的开关语义。
	if isVaultContainerList(cmd.SourceListType) || isVaultContainerList(cmd.DestinationListType) {
		cmd.ItemPlacement = req.InventoryItemPlacement
	}
	cmd.DestinationSlotIndex = parsed.DestinationSlotIndex
	cmd.DestinationInstance = parsed.DestinationInstanceValue
	cmd.DestinationStack = parsed.DestinationStack
	cmd.ActorIndex = parsed.ActorIndex
	cmd.TrailingState0 = parsed.TrailingState0
	cmd.TrailingState1 = parsed.TrailingState1
	return cmd
}

// NewSortCommand 构造整理背包命令计划。
func NewSortCommand(req alignedcmd.Request, parsed SortItemRequest) Command {
	cmd := baseCommand(req, "sort_item", "inventory owner + deterministic sort + backpack refresh order")
	cmd.SourceListType = parsed.ListType
	cmd.Category = parsed.Category
	cmd.Condition = parsed.Condition
	return cmd
}

// NewBuyCommand 构造 NPC 购买命令计划。
func NewBuyCommand(req alignedcmd.Request, parsed BuyItemRequest) Command {
	cmd := baseCommand(req, "buy_item", "inventory owner + wallet owner + shop validation + mutation id + popup/NOTI order")
	cmd.ItemTemplateID = parsed.ItemTemplateID
	cmd.Count = parsed.Count
	return cmd
}

// NewSellCommand 构造出售物品命令计划。
func NewSellCommand(req alignedcmd.Request, parsed DeleteOrSellRequest) Command {
	cmd := baseCommand(req, "sell_item", "inventory owner + wallet owner + mutation id + backpack/USERINFO/NOTI order")
	cmd.SourceListType = parsed.ListType
	cmd.SourceSlotIndex = parsed.SlotIndex
	cmd.Count = int32(parsed.Count)
	return cmd
}

// NewRepairCommand 构造修理装备命令计划。
func NewRepairCommand(req alignedcmd.Request, parsed RepairEquipmentRequest) Command {
	cmd := baseCommand(req, "repair_equipment", "equipment owner + wallet owner + durability persistence + USERINFO/NOTI order")
	cmd.SourceListType = parsed.InvenType
	cmd.SourceSlotIndex = parsed.SlotIndex
	cmd.RepairSlot = parsed.RepairItemSlot
	cmd.AutoRepair = parsed.AutoRepair
	cmd.QuickRepair = parsed.QuickRepair
	return cmd
}

// NewDisjointCommand 构造分解物品命令计划。
func NewDisjointCommand(req alignedcmd.Request, parsed DisjointItemRequest) Command {
	cmd := baseCommand(req, "disjoint_item", "inventory owner + disjoint reward owner + mutation id + ACK/backpack/popup order")
	cmd.TargetSlotIndex = parsed.TargetSlotIndex
	cmd.ItemSpace = parsed.ItemSpace
	cmd.DisjointItemSlotIndex = parsed.DisjointItemSlotIndex
	cmd.ContextValue = parsed.ContextValue
	return cmd
}

// NewUseStackableCommand 构造使用消耗品命令计划。
func NewUseStackableCommand(req alignedcmd.Request, parsed UseStackableRequest) Command {
	cmd := baseCommand(req, "use_stackable", "inventory owner + effect/reward owner + mutation id + USERINFO/NOTI order")
	cmd.SourceListType = parsed.ListType
	cmd.SourceSlotIndex = parsed.SlotIndex
	cmd.SourceInstanceValue = parsed.InstanceValue
	cmd.ItemTemplateID = parsed.ItemCode
	cmd.ReservedValue = parsed.Reserved
	cmd.StackableUseProvenanceResolver = req.StackableUseProvenanceResolver
	return cmd
}

// NewUseSharedEffectItemCommand 构造 op251（共享特效道具使用）命令计划。
// 与 op44 共用同一条 owner 路径：解析出来的 itemCode/slot 就是 op44 需要的
// 物品身份与源格，唯一区别是回包 opcode 由调用方按 op251 生成。
func NewUseSharedEffectItemCommand(req alignedcmd.Request, parsed UseSharedEffectItemRequest) Command {
	cmd := baseCommand(req, "use_shared_effect_item", "inventory owner + effect/reward owner + mutation id + ACK/slot refresh order")
	cmd.SourceListType = parsed.ListType
	cmd.SourceSlotIndex = parsed.SlotIndex
	cmd.ItemTemplateID = parsed.ItemCode
	cmd.StackableUseProvenanceResolver = req.StackableUseProvenanceResolver
	return cmd
}

func NewUseStackableActionCommand(req alignedcmd.Request, parsed UseStackableActionRequest) Command {
	cmd := baseCommand(req, "use_stackable_action", "character + inventory atomic owner + runtime-PVF damage-font definition")
	cmd.SourceListType = parsed.ListType
	cmd.SourceSlotIndex = parsed.SourceSlotIndex
	cmd.ActionIndex = parsed.ActionIndex
	return cmd
}

func NewSelectDamageFontCommand(req alignedcmd.Request, parsed SelectDamageFontRequest) Command {
	cmd := baseCommand(req, "select_damage_font", "character asset owner + owned/unexpired damage-font state")
	cmd.DamageFontIndex = parsed.FontIndex
	return cmd
}

// NewUpgradeCommand 构造强化或增幅命令计划。
func NewUpgradeCommand(req alignedcmd.Request, parsed UpgradeItemRequest) Command {
	cmd := baseCommand(req, "upgrade_item", "equipment owner + wallet/material owner + mutation id + reinforce result/USERINFO order")
	cmd.Mode = parsed.Mode
	cmd.TargetSlotIndex = parsed.TargetSlotIndex
	cmd.TargetItemTemplateID = parsed.TargetItemTemplateID
	cmd.MaterialSlotIndex = parsed.MaterialSlotIndex
	cmd.OptionalTicketSlot = parsed.OptionalTicketSlotIndex
	cmd.TargetItemName = parsed.TargetItemName
	cmd.UpgradeNoticeLevel = -1
	cmd.UpgradeCostResolver = req.UpgradeCostResolver
	cmd.UpgradeNPCPresent = parsed.NPCContextPresent
	cmd.UpgradeNPCID = parsed.NPCTemplateID
	cmd.UpgradeNPCMaterialSlot = parsed.NPCMaterialSlotIndex
	cmd.UpgradeProtectTicketResolver = req.UpgradeProtectTicketResolver
	return cmd
}

// NewSeparateUpgradeCommand 构造锻造（别升级 / separate upgrade，op438）命令计划。
// 请求体已由 dnfbridge/forging_codec.go 解出（协议形状归桥接层），这里只搬字段。
func NewSeparateUpgradeCommand(req alignedcmd.Request, forging *alignedcmd.ForgingRequest) Command {
	cmd := baseCommand(req, "upgrade_item_separate", "inventory owner + equipment owner + runtime-PVF separate-upgrade table + class1 438 ACK")
	if forging != nil {
		cmd.TargetListType = forging.List
		cmd.TargetSlotIndex = forging.Slot
		cmd.TargetItemTemplateID = int32(forging.ItemID)
		cmd.MaterialSlotIndex = forging.MaterialSlot
		cmd.TargetItemName = string(forging.Name)
		cmd.ForgingConfirmation = forging.Confirmation
	}
	return cmd
}

// NewEnchantCommand 构造宝珠附魔命令计划。
func NewEnchantCommand(req alignedcmd.Request, parsed EnchantByBeadRequest) Command {
	cmd := baseCommand(req, "enchant_by_bead", "inventory owner + equipment owner + mutation id + equipment/USERINFO/NOTI order")
	cmd.BeadListType = parsed.BeadListType
	cmd.BeadSlotIndex = parsed.BeadSlotIndex
	cmd.TargetListType = parsed.TargetListType
	cmd.TargetSlotIndex = parsed.TargetSlotIndex
	return cmd
}

func NewPurifyItemCommand(req alignedcmd.Request, parsed PurifyItemRequest) Command {
	cmd := baseCommand(req, "purify_item", "inventory owner + runtime PVF amplify config + atomic target/material mutation + current-EXE op204 ACK")
	cmd.TargetSlotIndex = parsed.TargetSlotIndex
	cmd.TargetItemTemplateID = parsed.TargetItemTemplateID
	cmd.MaterialSlotIndex = parsed.MaterialSlotIndex
	cmd.MaterialItemTemplateID = parsed.MaterialItemTemplateID
	return cmd
}

func NewInvestItemAmplifyOptionCommand(req alignedcmd.Request, parsed InvestItemAmplifyOptionRequest) Command {
	cmd := baseCommand(req, "invest_item_amplify_option", "inventory owner + runtime PVF amplify config + atomic target/material mutation + current-EXE op205 ACK")
	cmd.AmplifyAction = parsed.Action
	cmd.TargetSlotIndex = parsed.TargetSlotIndex
	cmd.TargetItemTemplateID = parsed.TargetItemTemplateID
	cmd.MaterialSlotIndex = parsed.MaterialSlotIndex
	cmd.MaterialItemTemplateID = parsed.MaterialItemTemplateID
	cmd.SelectedAmplifyOption = parsed.SelectedOption
	cmd.TargetItemName = parsed.TargetItemName
	return cmd
}

func baseCommand(req alignedcmd.Request, operation string, needs string) Command {
	return Command{
		Operation:           operation,
		AccountID:           strings.TrimSpace(req.AccountID),
		SelectedCharacterID: req.SelectedCharacterID,
		NeedsOwner:          needs,
	}
}

// NewUnsealRandomOptionCommand constructs the current op401 mutation plan.
func NewUnsealRandomOptionCommand(req alignedcmd.Request, parsed UnsealRandomOptionRequest) Command {
	cmd := baseCommand(req, "unseal_random_option", "inventory + character wallet atomic owner + runtime PVF")
	cmd.TargetSlotIndex = parsed.TargetSlotIndex
	cmd.InventoryManagerState = parsed.InventoryManagerState
	return cmd
}

// NewChangeRandomOptionCommand constructs the current op437 mutation plan.
func NewChangeRandomOptionCommand(req alignedcmd.Request, parsed ChangeRandomOptionRequest) Command {
	cmd := baseCommand(req, "change_random_option", "inventory + character wallet atomic owner + runtime PVF")
	cmd.TargetSlotIndex = parsed.TargetSlotIndex
	cmd.ReservedValue = uint32(parsed.Reserved)
	cmd.RandomOptionIndex = parsed.OptionIndex
	return cmd
}

func (c Command) String() string {
	base := fmt.Sprintf("account=%q char=%d", c.AccountID, c.SelectedCharacterID)
	switch c.Operation {
	case "delete_item", "sell_item":
		return fmt.Sprintf("%s list=%d slot=%d count=%d entries=%d item=%d needs=%s", base, c.SourceListType, c.SourceSlotIndex, c.Count, c.EntryCount, c.ItemTemplateID, c.NeedsOwner)
	case "move_itemspace":
		return fmt.Sprintf("%s src=(%d,%d,0x%08X) count=%d dst=(%d,%d,0x%08X) dstStack=%d actor=%d tail=(%d,%d) needs=%s", base, c.SourceListType, c.SourceSlotIndex, uint32(c.SourceInstanceValue), c.MoveCount, c.DestinationListType, c.DestinationSlotIndex, uint32(c.DestinationInstance), c.DestinationStack, c.ActorIndex, c.TrailingState0, c.TrailingState1, c.NeedsOwner)
	case "sort_item":
		return fmt.Sprintf("%s list=%d category=%d condition=%d needs=%s", base, c.SourceListType, c.Category, c.Condition, c.NeedsOwner)
	case "buy_item":
		return fmt.Sprintf("%s item=%d count=%d needs=%s", base, c.ItemTemplateID, c.Count, c.NeedsOwner)
	case "repair_equipment":
		return fmt.Sprintf("%s list=%d slot=%d repairSlot=%d auto=%t quick=%t needs=%s", base, c.SourceListType, c.SourceSlotIndex, c.RepairSlot, c.AutoRepair, c.QuickRepair, c.NeedsOwner)
	case "disjoint_item":
		return fmt.Sprintf("%s targetSlot=%d itemSpace=%d disjointSlot=%d ctx=0x%08X needs=%s", base, c.TargetSlotIndex, c.ItemSpace, c.DisjointItemSlotIndex, uint32(c.ContextValue), c.NeedsOwner)
	case "use_stackable":
		return fmt.Sprintf("%s list=%d slot=%d instance=0x%08X item=0x%08X reserved=0x%08X needs=%s", base, c.SourceListType, c.SourceSlotIndex, uint32(c.SourceInstanceValue), uint32(c.ItemTemplateID), c.ReservedValue, c.NeedsOwner)
	case "upgrade_item":
		if c.UpgradePolicyError != "" {
			return fmt.Sprintf("%s mode=%s target=(%d,%d) materialSlot=%d materialPVF=(%d,%d) policyErr=%q ticket=%d name=%q needs=%s", base, c.Mode, c.TargetSlotIndex, c.TargetItemTemplateID, c.MaterialSlotIndex, c.UpgradeMaterialItemID, c.UpgradeMaterialCount, c.UpgradePolicyError, c.OptionalTicketSlot, c.TargetItemName, c.NeedsOwner)
		}
		return fmt.Sprintf("%s mode=%s target=(%d,%d) materialSlot=%d materialPVF=(%d,%d) ticket=%d name=%q needs=%s", base, c.Mode, c.TargetSlotIndex, c.TargetItemTemplateID, c.MaterialSlotIndex, c.UpgradeMaterialItemID, c.UpgradeMaterialCount, c.OptionalTicketSlot, c.TargetItemName, c.NeedsOwner)
	case "enchant_by_bead":
		return fmt.Sprintf("%s bead=(%d,%d) target=(%d,%d) needs=%s", base, c.BeadListType, c.BeadSlotIndex, c.TargetListType, c.TargetSlotIndex, c.NeedsOwner)
	case "purify_item":
		return fmt.Sprintf("%s target=(%d,%d) material=(%d,%d) needs=%s", base, c.TargetSlotIndex, c.TargetItemTemplateID, c.MaterialSlotIndex, c.MaterialItemTemplateID, c.NeedsOwner)
	case "invest_item_amplify_option":
		return fmt.Sprintf("%s action=%d target=(%d,%d) material=(%d,%d) selected=%d name=%q needs=%s", base, c.AmplifyAction, c.TargetSlotIndex, c.TargetItemTemplateID, c.MaterialSlotIndex, c.MaterialItemTemplateID, c.SelectedAmplifyOption, c.TargetItemName, c.NeedsOwner)
	case "unseal_random_option":
		return fmt.Sprintf("%s targetSlot=%d managerState=0x%04X needs=%s", base, c.TargetSlotIndex, c.InventoryManagerState, c.NeedsOwner)
	case "change_random_option":
		return fmt.Sprintf("%s targetSlot=%d reserved=0x%04X optionIndex=%d needs=%s", base, c.TargetSlotIndex, c.ReservedValue, c.RandomOptionIndex, c.NeedsOwner)
	case "upgrade_item_separate":
		return fmt.Sprintf("%s target=(%d,%d,item=%d) materialSlot=%d confirmation=%d name=%q needs=%s", base, c.TargetListType, c.TargetSlotIndex, uint32(c.TargetItemTemplateID), c.MaterialSlotIndex, c.ForgingConfirmation, c.TargetItemName, c.NeedsOwner)
	default:
		return fmt.Sprintf("%s needs=%s", base, c.NeedsOwner)
	}
}
