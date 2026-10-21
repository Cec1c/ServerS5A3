// 本文件解析公会合成 C2S 请求体。
//
// op835（守护珠合成）与 op864（勋章合成）的发送器交给 writer 的 body 都是
// 37 字节：前 13 字节是发送函数未赋值的原生前缀区，业务字段从 +0x0D 开始。
// 本解析器只按固定 v151 发送器的直接指令证据核对长度、件数范围和空位标记；
// 前缀内容保持不透明，四个材料标识也不在这里解释成背包槽位或物品 ID。
package guildcompound

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// RequestBodyLen 是 op835/op864 追加 body 的已确认长度（0x25）。
	RequestBodyLen = 37
	// NativePrefixLen 是 body 前部尚未赋值的原生前缀区长度。
	NativePrefixLen = 13
	// MaterialRefCount 是业务字段中四个连续的 32 位材料标识。
	MaterialRefCount = 4
)

const (
	// AbsentMaterialRef 是发送器为空槽写入的标记。
	AbsentMaterialRef uint32 = 0xFFFFFFFF
	// MaxMaterialCount 是发送器扫描 helper 可能返回的最大已选件数。
	MaxMaterialCount uint32 = 4
)

const (
	countOffset  = NativePrefixLen
	gradeOffset  = countOffset + 4
	materialBase = gradeOffset + 4
)

// Request 是 op835/op864 的已解码请求。
type Request struct {
	// Prefix 保留业务字段之前的 13 字节。发送器在该区间内没有写入，内容、
	// 固定值以及是否由通用层剥离均未证实；解析器只复制，不解释也不校验。
	Prefix [NativePrefixLen]byte
	// Count 是 body +0x0D 的 u32 已选材料件数。
	Count uint32
	// Grade 是 body +0x11 的 u32 当前档位。
	Grade uint32
	// MaterialRefs 保留 body +0x15 起的四个 u32 原始材料引用。窗口写入者
	// 保存容器索引（并非模板 ID）；与实际背包槽位的映射仍需 owner 验证，
	// 因此这里不把字段命名为 slot 或 itemID。
	MaterialRefs [MaterialRefCount]uint32
}

var (
	// ErrRequestBodyLength 表示 body 不是发送器确认的 37 字节。
	ErrRequestBodyLength = errors.New("guild compound request body length must be 37")
	// ErrMaterialCount 表示件数超出已证实的 0..4 结构范围。
	ErrMaterialCount = errors.New("guild compound request material count must not exceed 4")
	// ErrUnusedMaterialRef 表示 count 之后的空槽没有携带 0xFFFFFFFF 标记。
	ErrUnusedMaterialRef = errors.New("guild compound request unused material slot must be 0xFFFFFFFF")
)

// DecodeRequest 解析 op835/op864 共用的 37 字节追加 body。
//
// 解析只依赖已确认的固定偏移，并在读取前要求精确长度，避免短 body 切片越界。
// 件数按发送器 helper 的 0..4 结构范围接受；2..4 的业务下限、材料标识的
// 实际含义、grade 范围以及件数与档位的组合规则均未证实，本函数不做判断。
func DecodeRequest(body []byte) (Request, error) {
	if len(body) != RequestBodyLen {
		return Request{}, fmt.Errorf("%w: got %d", ErrRequestBodyLength, len(body))
	}

	var request Request
	copy(request.Prefix[:], body[:NativePrefixLen])
	request.Count = binary.LittleEndian.Uint32(body[countOffset:gradeOffset])
	request.Grade = binary.LittleEndian.Uint32(body[gradeOffset:materialBase])
	for index := range request.MaterialRefs {
		offset := materialBase + index*4
		request.MaterialRefs[index] = binary.LittleEndian.Uint32(body[offset : offset+4])
	}

	if request.Count > MaxMaterialCount {
		return Request{}, fmt.Errorf("%w: got %d", ErrMaterialCount, request.Count)
	}
	for index := int(request.Count); index < MaterialRefCount; index++ {
		if request.MaterialRefs[index] != AbsentMaterialRef {
			return Request{}, fmt.Errorf("%w: index=%d value=0x%08X", ErrUnusedMaterialRef, index, request.MaterialRefs[index])
		}
	}
	return request, nil
}
