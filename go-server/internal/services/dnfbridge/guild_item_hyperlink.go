package dnfbridge

const currentGuildItemHyperlinkNotifyOpcode = uint16(371)

// V183 callback 2A04520 reads named messages without a scene actor lookup.
// Type6 has two decoration bytes; 29EFE20 reads count and each 148-byte row.
func buildCurrentGuildItemHyperlinkNoticeBody(channel byte, sender, message []byte, items [][]byte) []byte {
	var w packetWriter
	w.writeBytes([]byte{currentGuildChatType, 0, 0, 0})
	w.writeRawDstr(sender)
	w.writeByte(channel)
	w.writeRawDstr(message)
	w.writeByte(byte(len(items)))
	for _, item := range items {
		w.writeBytes(item)
	}
	w.writeRawDstr(nil)
	return w.bytes()
}
