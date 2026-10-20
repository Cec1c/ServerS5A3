// 本文件解析角色选择链 C2S 请求体。
// 字段顺序参考 C# CharacterSelectHandler；当前只用于 fallback 日志和后续 owner 化计划。
package character

import (
	"encoding/binary"
	"fmt"
)

// SelectCharacterRequest 描述选择角色请求。
type SelectCharacterRequest struct {
	SlotOrCharacterID uint16
}

// CreateCharacterRequest 描述创建角色请求。
type CreateCharacterRequest struct {
	Job  byte
	Name string
}

// DeleteCharacterRequest 描述删除角色请求。
type DeleteCharacterRequest struct {
	Slot uint16
	Name string
}

// CheckNameRequest 描述角色名查重请求。
type CheckNameRequest struct {
	Name string
}

// DecodeSelectCharacterRequest 解析 C# 选角请求的前 2 字节槽位。
func DecodeSelectCharacterRequest(body []byte) (SelectCharacterRequest, error) {
	if len(body) < 2 {
		return SelectCharacterRequest{}, fmt.Errorf("body too short: got %d want >= 2", len(body))
	}
	return SelectCharacterRequest{SlotOrCharacterID: binary.LittleEndian.Uint16(body)}, nil
}

// DecodeCreateCharacterRequest 解析创建角色请求：u8 job + i32 nameLen + name bytes。
func DecodeCreateCharacterRequest(body []byte) (CreateCharacterRequest, error) {
	if len(body) < 6 {
		return CreateCharacterRequest{}, fmt.Errorf("body too short: got %d want >= 6", len(body))
	}
	nameLen := int(int32(binary.LittleEndian.Uint32(body[1:])))
	if nameLen < 0 || 5+nameLen+1 > len(body) {
		return CreateCharacterRequest{}, fmt.Errorf("name length %d invalid for body %d", nameLen, len(body))
	}
	return CreateCharacterRequest{
		Job:  body[0],
		Name: string(body[5 : 5+nameLen]),
	}, nil
}

// DecodeDeleteCharacterRequest 解析删除角色请求：u16 slot + i32 nameLen + name bytes。
//
// 真实 wire 形态 = [slot:u16 LE][nameLen:u32 LE @2][name @6]，两处独立证据：
//  1. 2026-09-28 现场抓包 body（17 字节）= 02 00 | 0b000000 | "test78_copy"
//     （_dist/rename-delete-diagnosis.md §3.2，与 DB 行 slot=2/name=test78_copy 对合）；
//  2. 厂商 CharacterSelectHandler.cs:935-937 明写同一格式，并点名
//     "[slot:u8][nameLen@1] 会把 nameLen 错读为 768" 这个同源 bug。
//
// 旧实现正是那个错读：body[1:] 前 4 字节 = 00 0b 00 00 = 2816，于是现场日志
// 恒报 "name length 2816 invalid for body 17"。这里的 30 字节上限与厂商 :940 一致。
func DecodeDeleteCharacterRequest(body []byte) (DeleteCharacterRequest, error) {
	if len(body) < 6 {
		return DeleteCharacterRequest{}, fmt.Errorf("body too short: got %d want >= 6", len(body))
	}
	slot := binary.LittleEndian.Uint16(body[0:2])
	nameLen := int(int32(binary.LittleEndian.Uint32(body[2:6])))
	if nameLen <= 0 || nameLen > 30 || 6+nameLen > len(body) {
		return DeleteCharacterRequest{}, fmt.Errorf("name length %d invalid for body %d", nameLen, len(body))
	}
	return DeleteCharacterRequest{
		Slot: slot,
		Name: string(body[6 : 6+nameLen]),
	}, nil
}

// DecodeCheckNameRequest 解析角色名查重请求：i32 nameLen + name bytes。
func DecodeCheckNameRequest(body []byte) (CheckNameRequest, error) {
	if len(body) < 5 {
		return CheckNameRequest{}, fmt.Errorf("body too short: got %d want >= 5", len(body))
	}
	nameLen := int(int32(binary.LittleEndian.Uint32(body)))
	if nameLen <= 0 || 4+nameLen > len(body) {
		return CheckNameRequest{}, fmt.Errorf("name length %d invalid for body %d", nameLen, len(body))
	}
	return CheckNameRequest{Name: string(body[4 : 4+nameLen])}, nil
}

func (r SelectCharacterRequest) String() string {
	return fmt.Sprintf("slotOrChar=%d", r.SlotOrCharacterID)
}

func (r CreateCharacterRequest) String() string {
	return fmt.Sprintf("job=%d name=%q", r.Job, r.Name)
}

func (r DeleteCharacterRequest) String() string {
	return fmt.Sprintf("slot=%d name=%q", r.Slot, r.Name)
}

func (r CheckNameRequest) String() string {
	return fmt.Sprintf("name=%q", r.Name)
}
