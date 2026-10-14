package dnfbridge

import (
	"encoding/binary"
	"fmt"
)

// 当前客户端（DNF.exe 8bf1d39d…）锻造 / 别升级（separate upgrade）族的线协议。
// DOC-087/092：锻造动画卡住、窗口无法关闭、锻造器——基线 5fc6807 只有 438 的枚举名，
// 没有任何 handler，客户端上行因此永远等不到回包。
//
// C2S 438（0x1b6）：发送端 0x25fae0c/0x25fae16 开包后按序写：
//
//	u8  list          （0x25fae4d → writer 0x374f390）
//	u16 slot          （0x25fae5a → writer 0x374f3c0）
//	u32 itemTemplate  （0x25fae67 → writer 0x374f3f0）
//	u16 materialSlot  （0x25fae77 → writer 0x374f3c0）
//	u32 名字长度 + 名字字节（0x25faebe → 0x374f550 → 0x374f4c0，长度前缀 + 无 NUL）
//	u8  confirmation  （0x25faece → writer 0x374f390，值来自发送函数参数 [ebp+0xc]）
//
// 现场形状（main slot 9、item 101011368、材料槽 121、两字节名字、confirmation 0）：
//
//	00 0900 a84f0506 7900 02000000 ced0 00
//
// class1 438 响应：注册 0x447a740（push 0x1b6 → 0x251e1c0 → 0x2524210/0x25242a0），
// 回调 0x24836b0。先 `cmp byte ptr [ebp+0xc], 0; je 0x2483b60`（调度器已消费成功字节），
// 成功时 0x2483751..0x24837b6 按序读 u16 materialSlot、u32 materialRemaining、u8 oldLevel、
// u8 outcome、u8 newLevel、u8 targetList、u16 targetSlot ⇒ 回包体 13 字节（outcome 0=成功、
// 1=掷点失败）。失败走 0x2483b60：先 0x12030a0 复位锻造 UI，再按错误码查表显示消息。
//
// 注意：class0 438（0x200e590）是**另外一条**只读 u16 的通知，与本族的 class1 回包无关，
// 绝不能拿来当锻造 ACK。
const (
	// currentForgingRequestPrefixSize 是 list/slot/item/material 四段的总长。
	currentForgingRequestPrefixSize = 9
	// currentForgingRequestMaxNameBytes 是防御性上限（不是客户端契约）：
	// 名字只用于确认框显示，过长的长度前缀只能是畸形包。
	currentForgingRequestMaxNameBytes = 256
	// currentForgingAckBodySize = 1（成功字节）+ u16 + u32 + u8*4 + u16。
	currentForgingAckBodySize = 13
)

// currentForgingRequest 是 C2S 438 的解码结果。名称与 confirmation 只做显示/旁路，
// 不参与域校验：confirmation 的取值域没有证据（发送端把它当参数传入，且全 EXE 扫不到
// 直接调用点，见 Docs/D33_FORGING_20261014.md），因此不按 0..2 猜测限制。
type currentForgingRequest struct {
	List         byte
	Slot         int16
	ItemID       uint32
	MaterialSlot int16
	Name         []byte
	Confirmation byte
}

// decodeCurrentForgingRequest 解析 438 请求体；只对**有证据**的形状做校验。
func decodeCurrentForgingRequest(body []byte) (currentForgingRequest, error) {
	if len(body) < currentForgingRequestPrefixSize+4+1 {
		return currentForgingRequest{}, fmt.Errorf("forging request too short: %d", len(body))
	}
	nameLength := int(binary.LittleEndian.Uint32(body[9:13]))
	if nameLength < 0 || nameLength > currentForgingRequestMaxNameBytes || nameLength+14 != len(body) {
		return currentForgingRequest{}, fmt.Errorf("forging request name length %d does not match body %d", nameLength, len(body))
	}
	request := currentForgingRequest{
		List:         body[0],
		Slot:         int16(binary.LittleEndian.Uint16(body[1:3])),
		ItemID:       binary.LittleEndian.Uint32(body[3:7]),
		MaterialSlot: int16(binary.LittleEndian.Uint16(body[7:9])),
		Name:         append([]byte(nil), body[13:13+nameLength]...),
		Confirmation: body[len(body)-1],
	}
	if (request.List != 0 && request.List != 3) || request.Slot < 0 || request.MaterialSlot < 0 ||
		request.ItemID == 0 || (request.List == 0 && request.Slot == request.MaterialSlot) {
		return currentForgingRequest{}, fmt.Errorf("forging inventory endpoint is invalid: list=%d slot=%d material=%d item=%d",
			request.List, request.Slot, request.MaterialSlot, request.ItemID)
	}
	return request, nil
}

// buildCurrentForgingAck 组装 class1 438 的回包体：
//
//	u8 1（成功字节，由调度器消费） | u16 materialSlot | u32 materialRemaining |
//	u8 oldLevel | u8 outcome（0=成功、1=掷点失败） | u8 newLevel | u8 targetList | u16 targetSlot
//
// outcome 只区分「成功」与「掷点失败」；参数/材料类拒绝要回的是失败体 `0 + u8 error`，
// 那条路径的错误码枚举尚未取证，本函数不生成它。
func buildCurrentForgingAck(request currentForgingRequest, remaining uint32, oldLevel, newLevel byte, succeeded bool) []byte {
	body := make([]byte, currentForgingAckBodySize)
	body[0] = 1
	binary.LittleEndian.PutUint16(body[1:3], uint16(request.MaterialSlot))
	binary.LittleEndian.PutUint32(body[3:7], remaining)
	body[7] = oldLevel
	if !succeeded {
		body[8] = 1
	}
	body[9] = newLevel
	body[10] = request.List
	binary.LittleEndian.PutUint16(body[11:13], uint16(request.Slot))
	return body
}
