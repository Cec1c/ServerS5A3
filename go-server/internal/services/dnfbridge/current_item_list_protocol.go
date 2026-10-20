package dnfbridge

import (
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func buildCurrentItemListBody(listType byte, entries []currentItemListEntry, state dnfrepo.CharacterContainerState) []byte {
	var writer packetWriter
	writer.writeByte(listType)
	switch listType {
	case 0:
		writer.writeUint16(state.MainSlotCount)
	case 1:
		writer.writeUint16(state.AvatarExpansion)
	case 2:
		writer.writeUint16(state.PersonalCargoSlotCount)
	case 12:
		writer.writeUint16(state.AccountCargoSelectionKey)
		writer.writeUint32(state.AccountCargoStateValue)
	}
	writer.writeUint16(uint16(len(entries)))
	for _, entry := range entries {
		writer.writeBytes(entry.data[:])
		if listType == 1 {
			// 【2026-09-24 紧急回滚（临时）】list 1（装扮栏）的**页面行**必须继续带
			// (socket,color) 两段长度前缀，即每行 0x77 + 8 = 127 字节。
			//
			// 现场证据：收尾批次把这里从 127 改成 0x77 之后，全量封包日志里**唯一一次**
			// 用 119 字节行发 list 1 页的登录（22:04 conn game-000003，
			// packet_log.20260924-220805.txt，`entry_size=119`，其余日志全是 127）
			// 客户端当场 0xC0000005：eip=0x03B10747、返回地址 0x034E3AD9（紧跟
			// 0x034E3AC0 那条"定长读 0x77 行"之后），读地址 0x001B0000 是页缓冲之外的
			// 野地址 —— 读端把下一行的行首当成 blob 长度前缀，走出页缓冲。
			// 同时那次登录是"上线下线都没问题"的一天里唯一一次崩溃登录。
			//
			// 恢复计划：装扮页"同一套时装显示多份"必须用**客户端实测**重新定契约
			// （A/B 两次重登：127 / 119 各一次截图对比），不能只靠反汇编推断；
			// 在那之前保持 127（即本回滚）。详见 changlog.md 顶部同名那一节。
			writeCurrentAvatarItemListOptionalBlobs(&writer, entry)
		}
	}
	if listType == 2 {
		// Current sub_1D72380 always consumes a cargo grouping trailer after
		// the ordinary rows: u8 groupCount followed by groupCount*raw[8].
		// Group persistence is not owned yet, so terminate the proved grammar
		// with the constructor-safe empty group list.
		writer.writeByte(0)
	}
	return writer.bytes()
}

// currentItemListEntrySizeForType 是"容器页(msg 13)"里每行的固定字节数。
// list 1 的页面行带两段长度前缀的可选 blob（0x77 + 4 + 4 = 127），
// 其它 list 的行就是裸的 0x77（与 msg 14 单行载体共用这两段前缀）。
//
// 本函数只用于日志与用例的"行宽自证"，不参与组包；组包在 buildCurrentItemListBody 里。
func currentItemListEntrySizeForType(listType byte) int {
	if listType == 1 {
		return currentItemListEntryWireSize + 8
	}
	return currentItemListEntryWireSize
}

func writeCurrentAvatarItemListOptionalBlobs(writer *packetWriter, entry currentItemListEntry) {
	if writer == nil {
		return
	}
	writeCurrentItemListOptionalBlob(writer, entry.avatarSocketData)
	writeCurrentItemListOptionalBlob(writer, entry.avatarColorData)
}

func writeCurrentItemListOptionalBlob(writer *packetWriter, data []byte) {
	if writer == nil {
		return
	}
	if len(data) == 0 {
		writer.writeUint32(0)
		return
	}
	writer.writeUint32(uint32(len(data)))
	writer.writeBytes(data)
}

func buildCurrentEquipmentUpdateBody(listType byte, entries []currentEquipmentUpdateEntry) []byte {
	var writer packetWriter
	writer.writeByte(listType)
	writer.writeUint16(uint16(len(entries)))
	for _, entry := range entries {
		writer.writeBytes(entry.data[:])
	}
	return writer.bytes()
}
