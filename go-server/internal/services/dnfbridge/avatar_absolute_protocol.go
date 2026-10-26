package dnfbridge

import (
	"encoding/binary"
	"fmt"
)

const currentAbsoluteAvatarMaxMaterials = 8 // 原生请求/应答的固定槽位容量，并非PVF扣料数量。
const currentAbsoluteAvatarRequestSize = 144

type currentAbsoluteAvatarMaterial struct {
	Slot   int16
	ItemID uint32
}
type currentAbsoluteAvatarRequest struct {
	ConsumeSlot int16
	Target      uint32
	Ability     uint16
	Materials   []currentAbsoluteAvatarMaterial
}

func parseCurrentAbsoluteAvatarRequest(body []byte) (currentAbsoluteAvatarRequest, error) {
	// 原生界面发出144字节UI快照；前24字节含槽、目标、属性、数量，随后8条6字节材料。
	// 其余是客户端不透明快照，不作为服务端身份或规则依据。
	if len(body) != currentAbsoluteAvatarRequestSize && len(body) != currentAbsoluteAvatarRequestSize+4 {
		return currentAbsoluteAvatarRequest{}, fmt.Errorf("梦想请求长度%d无效", len(body))
	}
	r := currentAbsoluteAvatarRequest{ConsumeSlot: int16(binary.LittleEndian.Uint16(body[13:15])), Target: binary.LittleEndian.Uint32(body[16:20]), Ability: binary.LittleEndian.Uint16(body[20:22])}
	n := int(binary.LittleEndian.Uint16(body[22:24]))
	if r.ConsumeSlot < 0 || r.Target <= 1 || r.Ability > currentAvatarCompoundMaxAbilityNo || n < 1 || n > currentAbsoluteAvatarMaxMaterials {
		return currentAbsoluteAvatarRequest{}, fmt.Errorf("梦想请求槽位、目标、属性或材料数量无效")
	}
	seen := map[int16]bool{}
	for i := 0; i < n; i++ {
		off := 24 + 6*i
		m := currentAbsoluteAvatarMaterial{Slot: int16(binary.LittleEndian.Uint16(body[off:])), ItemID: binary.LittleEndian.Uint32(body[off+2:])}
		if m.Slot < 0 || m.ItemID == 0 || seen[m.Slot] {
			return currentAbsoluteAvatarRequest{}, fmt.Errorf("梦想材料槽重复或无效")
		}
		seen[m.Slot] = true
		r.Materials = append(r.Materials, m)
	}
	return r, nil
}

func buildCurrentAbsoluteAvatarSuccessBody(r currentAbsoluteAvatarRequest, slot int16) []byte {
	// 原生读者在成功标志后固定读54字节，结果对象使用下列字段偏移。
	body := make([]byte, 55)
	body[0] = 1
	b := body[1:]
	binary.LittleEndian.PutUint16(b[4:6], uint16(slot))
	binary.LittleEndian.PutUint32(b[6:10], r.Target)
	binary.LittleEndian.PutUint16(b[10:12], r.Ability)
	binary.LittleEndian.PutUint16(b[12:14], 1) // 每条装扮行的扣除数量。
	for i := 0; i < currentAbsoluteAvatarMaxMaterials; i++ {
		s := int16(-1)
		if i < len(r.Materials) {
			s = r.Materials[i].Slot
		}
		binary.LittleEndian.PutUint16(b[14+i*2:], uint16(s))
	}
	return body
}
