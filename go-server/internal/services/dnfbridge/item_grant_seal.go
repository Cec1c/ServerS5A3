package dnfbridge

import (
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// currentNewPVFEquipmentSealFlag 只用于创建新装备实例，参照86的
// InventoryCreateService.ApplyEquipmentDefaults，按PVF的[attach type]初始化封装。
// 库存投影、穿脱和交易不调用它，已使用的装备不能按模板重新封装。
// 宠物本体的同号字段属于宠物类型，由专属创建流程管理。
func currentNewPVFEquipmentSealFlag(definition dungeonDropItemDefinition) byte {
	if definition.Kind != dungeonDropItemEquipment || currentPVFItemTargetContainer(definition).PetBody {
		return 0
	}
	if normalizeMagicBoxPVFType(definition.AttachType) == "sealing" {
		return 1
	}
	return 0
}

// initializeCurrentSystemMailEquipmentSeal 只初始化有系统来源证据、尚未带实例行的
// 新发放附件。玩家邮件、已有原始行和明确解封/首次使用证据均保持；不能按模板
// 给任意邮件中的旧装备重新封装。旧GM邮件以既有source标记识别。
func initializeCurrentSystemMailEquipmentSeal(mail dnfrepo.MailRecord, attachment *dnfrepo.MailAttachment, definition dungeonDropItemDefinition) bool {
	if attachment == nil || currentNewPVFEquipmentSealFlag(definition) == 0 || attachment.Bind || len(attachment.RawEntry) != 0 {
		return false
	}
	if sender := strings.TrimSpace(mail.SenderCharacterID); sender != "" && sender != "0" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(mail.Metadata["system_mail"]), "true") && strings.TrimSpace(mail.Metadata["source"]) != "dnf90-gm-tool" {
		return false
	}
	for _, key := range []string{"seal_flag", "seal", "bind_flag", "bind", "seal_removed_by_first_equip", "trade_locked_by_first_equip"} {
		if _, exists := attachment.Extra[key]; exists {
			return false
		}
	}
	for _, key := range []string{"raw_entry_hex", "current_raw_entry_77", "raw_entry_77"} {
		if strings.TrimSpace(attachment.Extra[key]) != "" {
			return false
		}
	}
	if attachment.Extra == nil {
		attachment.Extra = make(map[string]string, 1)
	}
	attachment.Extra["seal_flag"] = "1"
	return true
}
