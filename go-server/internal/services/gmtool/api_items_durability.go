package gmtool

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// GM 面板发放邮件的附件证据（发放端）。
//
// 过去 `handleItemGrant` 只写 {ItemID, Count}：附件既没有 Extra、也没有 0x77 行。
// 装备类道具因此是"0 耐久"：
//   - GM 面板自己的角色背包视图（inventoryRowPayload）只读 stack.Extra["durability"]，
//     缺键就显示 0；
//   - 客户端场景物品行 0x0B 同样读这两个键，邮件附件行 0x0B 也一样；
//   - 服务端修理路径要求 durability + max_durability 同时存在，缺了就失败关闭
//     （inventory.repairDurabilityOf → ErrRepairDurabilityMissing、
//     equip.repairBagDurabilityOf → ErrRepairCostMissing），
//     所以领到背包的装备根本修不了。
//
// 耐久真源与 `dungeon_drop_catalog.ResolveItem` 完全一致：运行时 PVF 的
// [durability] / [maximum durability] / [max durability]。
// 堆叠物没有耐久语义（也不该有），这里用 Kind == "equipment" 硬门禁，绝不写。
//
// 发放端补齐后，存量旧邮件仍由 dnfbridge 的登录期修补
// （reconcileCurrentMailAttachmentExpirations）与领取端解析器兜底。

const (
	grantItemKindEquipment = "equipment"

	grantExtraKeyPVFKind       = "mailbox_pvf_kind"
	grantExtraKeyEquipmentType = "mailbox_equipment_type"
	grantExtraKeyPVFPath       = "mailbox_pvf_path"
	grantExtraKeyDurability    = "durability"
	grantExtraKeyMaxDurability = "max_durability"
)

// grantAttachmentExtra 组装发放时就要随附件落库的证据：
//   - mailbox_pvf_kind / mailbox_equipment_type：客户端按"道具类型"分叉编码附件行
//     （[creature] 走 type-26 分支），GM 邮件过去没有任何 Extra，线上表现为
//     "点开邮箱客户端卡死"；这里在发放时就按运行时 PVF 写对。
//   - durability / max_durability：只给装备/时装类道具写，取值是 PVF 的基础耐久
//     （新造实例按 PVF 语义是满耐久）。
//
// 返回 nil 表示没有可写的证据（例如 PVF 没有声明耐久的堆叠物）。
func grantAttachmentExtra(entry itemEntry, pvfDurability uint16) map[string]string {
	extra := make(map[string]string, 5)
	if kind := strings.TrimSpace(entry.Kind); kind != "" {
		extra[grantExtraKeyPVFKind] = kind
	}
	// entry.Tag 是物品索引里已经归一化的类型 token（去方括号、小写，见
	// items_index.firstItemTag），与 dnfbridge 登录期修补写入的 token 同形。
	if token := strings.TrimSpace(entry.Tag); token != "" {
		extra[grantExtraKeyEquipmentType] = token
	}
	if pvfPath := strings.TrimSpace(entry.PVFPath); pvfPath != "" {
		extra[grantExtraKeyPVFPath] = pvfPath
	}
	if entry.Kind == grantItemKindEquipment && pvfDurability > 0 {
		value := strconv.FormatUint(uint64(pvfDurability), 10)
		extra[grantExtraKeyDurability] = value
		extra[grantExtraKeyMaxDurability] = value
	}
	if len(extra) == 0 {
		return nil
	}
	return extra
}

// grantPVFDurability 按运行时 PVF 读取一件装备的基础耐久。
//
// 读不到就返回 0（= 没有耐久证据），**不阻断发放**：GM 面板不该因为一次 PVF 读取
// 失败而发不出道具，领取端 dnfbridge 的邮箱解析器还会按同一份 PVF 再补一次。
func (s *Server) grantPVFDurability(entry itemEntry) uint16 {
	if s == nil || entry.Kind != grantItemKindEquipment {
		return 0
	}
	document, err := s.grantPVFItemDocument(entry)
	if err != nil {
		if s.cfg.Logger != nil {
			s.cfg.Logger("dnf90 gm grant PVF durability unavailable",
				"item", entry.ID, "path", entry.PVFPath, "error", err)
		}
		return 0
	}
	// 与 dungeon_drop_catalog.ResolveItem 同一顺序与同一上限。
	for _, name := range []string{"durability", "maximum durability", "max durability"} {
		value, found := document.Int(name)
		if !found || value <= 0 {
			continue
		}
		if value > 65535 {
			value = 65535
		}
		return uint16(value)
	}
	return 0
}

// grantPVFItemDocument 按物品索引记录的 kind + PVFPath 打开运行时 PVF 文档，
// 路径归一化与 items_index.parseItemEntry 一致（lst 里是相对根目录的路径）。
func (s *Server) grantPVFItemDocument(entry itemEntry) (*dnfpvf.Document, error) {
	source, err := s.source()
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, errors.New("runtime PVF source is unavailable")
	}
	root := strings.TrimSpace(entry.Kind)
	path := strings.TrimSpace(entry.PVFPath)
	if path == "" {
		return nil, fmt.Errorf("item %d has no PVF path", entry.ID)
	}
	if root != "" && !strings.HasPrefix(strings.ToLower(path), strings.ToLower(root)+"/") {
		path = root + "/" + strings.TrimPrefix(path, "/")
	}
	text, err := source.ReadText(path)
	if err != nil {
		return nil, fmt.Errorf("read PVF item %d at %s: %w", entry.ID, path, err)
	}
	return dnfpvf.Parse(path, text)
}
