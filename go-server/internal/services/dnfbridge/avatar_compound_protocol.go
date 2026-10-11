package dnfbridge

import (
	"encoding/binary"
	"errors"
	"fmt"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 当前客户端的装扮合成（时装合成）C2S/S2C 形状。
//
// 请求体（22 字节）字段顺序有两条独立证据，且两者完全一致：
//
//   - 仓库既有解析 internal/modules/dnf/avatartitle/request.go:51-61；
//
//   - 同族既有实现 a21zigai/ServerS4A21 Server/DfoServer/Network/Handlers/
//     InventoryHandler.Package.cs:1931-1935（op 0x0063 = 99）。
//
//     [0:2]   i16 consumeSlot   主背包里的合成器消耗品槽位
//     [2:4]   i16 slot1         装扮栏第 1 件材料槽位
//     [4:8]   i32 itemID1       第 1 件材料 itemID（0 表示客户端未带）
//     [8:10]  i16 slot2         装扮栏第 2 件材料槽位
//     [10:14] i32 itemID2       第 2 件材料 itemID（0 表示客户端未带）
//     [14:18] i32 requestedItemID 玩家选定的结果外观 itemID
//     [18:20] u16 abilityNo     玩家选定的结果属性序号（etc/avatarabilitystringtable.etc 下标，0=[none]）
//     [20:22] u16 tail          客户端保留位
const (
	currentAvatarCompoundRequestSize       = 22
	currentAvatarCompoundLegacyTrailerSize = 4
	currentAvatarCompoundResultRowCount    = 2
	currentAvatarCompoundResultColorBytes  = 4
	currentAvatarCompoundRateScale         = 10000
	currentAvatarCompoundFailureCode       = 0x16
	currentAvatarCompoundMaxAbilityNo      = 0xff
)

var errCurrentAvatarCompoundRequestInvalid = errors.New("dnf current avatar compound request is invalid")

// currentAvatarCompoundRequest 是 op99 的请求语义。
type currentAvatarCompoundRequest struct {
	ConsumeSlot     int16
	Slot1           int16
	ItemID1         uint32
	Slot2           int16
	ItemID2         uint32
	RequestedItemID uint32
	AbilityNo       uint16
}

// currentAvatarCompoundResultRow 是一条结果装扮行，用于拼装成功回包。
type currentAvatarCompoundResultRow struct {
	Slot      int16
	ItemID    uint32
	AbilityNo uint16
	// Socket 是该件装扮的孔位缓冲（定长 currentAvatarSocketBytes）；
	// 新建装扮为空，长度不足时按零补齐。
	Socket []byte
}

func (r currentAvatarCompoundRequest) String() string {
	return fmt.Sprintf("consume=%d slot1=%d item1=%d slot2=%d item2=%d requested=%d ability=%d",
		r.ConsumeSlot, r.Slot1, r.ItemID1, r.Slot2, r.ItemID2, r.RequestedItemID, r.AbilityNo)
}

// stripLegacyAvatarCompoundTransportTrailer 去掉旧传输在语义体后附加的 4 字节尾部。
// 22 字节语义体 + 4 字节尾部是当前客户端的旧路由形态，非 22/26 的包体一律交给解析器拒绝。
func stripLegacyAvatarCompoundTransportTrailer(body []byte) []byte {
	if len(body) != currentAvatarCompoundRequestSize+currentAvatarCompoundLegacyTrailerSize {
		return body
	}
	return append([]byte(nil), body[:currentAvatarCompoundRequestSize]...)
}

// parseCurrentAvatarCompoundRequest 解析 op99 请求体；槽位与 itemID 必须自洽。
// 只读取前 22 字节：同族既有实现同样以 "body >= 22" 为界，客户端在语义体之后
// 追加的字段不参与判定，因此更长的包体不算失败。
func parseCurrentAvatarCompoundRequest(body []byte) (currentAvatarCompoundRequest, error) {
	if len(body) < currentAvatarCompoundRequestSize {
		return currentAvatarCompoundRequest{}, fmt.Errorf(
			"%w: op99 body=%d want>=%d",
			errCurrentAvatarCompoundRequestInvalid, len(body), currentAvatarCompoundRequestSize)
	}
	body = body[:currentAvatarCompoundRequestSize]
	request := currentAvatarCompoundRequest{
		ConsumeSlot:     int16(binary.LittleEndian.Uint16(body[0:2])),
		Slot1:           int16(binary.LittleEndian.Uint16(body[2:4])),
		ItemID1:         binary.LittleEndian.Uint32(body[4:8]),
		Slot2:           int16(binary.LittleEndian.Uint16(body[8:10])),
		ItemID2:         binary.LittleEndian.Uint32(body[10:14]),
		RequestedItemID: binary.LittleEndian.Uint32(body[14:18]),
		AbilityNo:       binary.LittleEndian.Uint16(body[18:20]),
	}
	if request.ConsumeSlot < 0 {
		return currentAvatarCompoundRequest{}, fmt.Errorf("%w: op99 consume_slot=%d", errCurrentAvatarCompoundRequestInvalid, request.ConsumeSlot)
	}
	if request.Slot1 < 0 || request.Slot2 < 0 || request.Slot1 == request.Slot2 {
		return currentAvatarCompoundRequest{}, fmt.Errorf(
			"%w: op99 slot1=%d slot2=%d", errCurrentAvatarCompoundRequestInvalid, request.Slot1, request.Slot2)
	}
	if request.RequestedItemID == 0 {
		return currentAvatarCompoundRequest{}, fmt.Errorf("%w: op99 requested_item=0", errCurrentAvatarCompoundRequestInvalid)
	}
	if request.AbilityNo > currentAvatarCompoundMaxAbilityNo {
		return currentAvatarCompoundRequest{}, fmt.Errorf("%w: op99 ability_no=%d", errCurrentAvatarCompoundRequestInvalid, request.AbilityNo)
	}
	return request, nil
}

// buildCurrentAvatarCompoundFailureBody 是 op99 的显式失败回包体。
//
// 形状证据：同族既有实现的 BuildCompoundAvatarErrorBody(includeTailByte: true)
// = 0x00 0x16 0x00（flag=0 + 错误码 0x16 + 客户端保留位）。
// 本客户端同族命令（op202 装扮分解、op256 徽章合成）已观测到的失败读法是
// "flag=0 + 错误码"，这里保留 op99 自身的第三个保留字节即可同时满足两种情况：
// 少读的客户端会忽略它，多读的客户端不会越界。
func buildCurrentAvatarCompoundFailureBody() []byte {
	return []byte{0x00, currentAvatarCompoundFailureCode, 0x00}
}

// buildCurrentAvatarCompoundSuccessBody 按 op99 的成功分支拼装结果回包。
//
// 形状证据：同族既有实现 InventoryHandler.Package.cs:1973-1998。
// 结构为：结果标志(1) + 消耗行数(3) + 三条"列表/槽位/数量"消耗行
// （装扮栏 slot1、装扮栏 slot2、主背包合成器槽位），随后固定两条结果行：
// 槽位(i16) + itemID(i32) + 保留(i32) + 属性(u16) + 装扮孔位缓冲(DSTR)
// + 颜色缓冲(DSTR)。结果行数固定为 2（选定外观 + 可能的额外稀有装扮），
// 缺位用 slot=-1/itemID=0 占位。
func buildCurrentAvatarCompoundSuccessBody(request currentAvatarCompoundRequest, rows []currentAvatarCompoundResultRow) []byte {
	var body packetWriter
	body.writeByte(0x01)
	body.writeByte(0x03)
	body.writeByte(currentExeInventoryAvatarListType)
	body.writeUint16(uint16(request.Slot1))
	body.writeUint32(1)
	body.writeByte(currentExeInventoryAvatarListType)
	body.writeUint16(uint16(request.Slot2))
	body.writeUint32(1)
	body.writeByte(dnfrepo.MainInventoryListType)
	body.writeUint16(uint16(request.ConsumeSlot))
	body.writeUint32(1)
	for index := 0; index < currentAvatarCompoundResultRowCount; index++ {
		row := currentAvatarCompoundResultRow{Slot: -1}
		if index < len(rows) {
			row = rows[index]
		}
		body.writeUint16(uint16(row.Slot))
		body.writeUint32(row.ItemID)
		body.writeUint32(0)
		body.writeUint16(row.AbilityNo)
		// 新建装扮没有镶嵌孔数据与颜色数据：写入与客户端逐行解析一致的
		// 定长长度前缀缓冲（30 字节装扮孔位 + 4 字节颜色）。
		body.writeInt32(currentAvatarSocketBytes)
		socket := make([]byte, currentAvatarSocketBytes)
		copy(socket, row.Socket)
		body.writeBytes(socket)
		body.writeInt32(currentAvatarCompoundResultColorBytes)
		body.writeZero(currentAvatarCompoundResultColorBytes)
	}
	return body.bytes()
}
