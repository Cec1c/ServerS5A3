package mail

import (
	"encoding/binary"
	r "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"strings"
)

// RestoreEquipmentAttachmentInstanceExtra 用已有普通装备的0x77实例行补缺失证据。
// 调用方须已按PVF排除装扮和宠物，不能把其同号字节当作装备耐久/品质。
// 已明确保存的字段优先；这里不生成随机数、不修复耐久、不覆盖附魔或徽章。
func RestoreEquipmentAttachmentInstanceExtra(a *r.MailAttachment) bool {
	if a == nil || len(a.RawEntry) != 119 || a.ItemID <= 0 || a.ItemID > int64(^uint32(0)) || binary.LittleEndian.Uint32(a.RawEntry[2:]) != uint32(a.ItemID) {
		return false
	}
	values := map[string]string{
		"quality_seed": strconv.FormatUint(uint64(binary.LittleEndian.Uint32(a.RawEntry[6:])), 10),
		"durability":   strconv.FormatUint(uint64(binary.LittleEndian.Uint16(a.RawEntry[11:])), 10),
		"ext_data0":    strconv.Itoa(int(a.RawEntry[10])),
		"reseal_count": strconv.Itoa(int(a.RawEntry[10] >> 5)),
		"seal_flag":    strconv.Itoa(int(a.RawEntry[13])),
	}
	changed := false
	extra := cloneStringMap(a.Extra)
	for k, v := range values {
		if strings.TrimSpace(extra[k]) == "" {
			if extra == nil {
				extra = map[string]string{}
			}
			extra[k] = v
			changed = true
		}
	}
	if changed {
		a.Extra = extra
	}
	return changed
}
