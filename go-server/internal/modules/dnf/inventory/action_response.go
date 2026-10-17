package inventory

// BuildStackableActionAck follows the matching EXE class1/op515 reader
// (RVA 0x20f3580). The action selects client-side item behavior; it is not a
// remaining count. Authoritative inventory projection follows in op14.
func BuildStackableActionAck(slot int16, list byte, action uint32) []byte {
	var w packetWriter
	w.writeByte(1)
	w.writeInt16(slot)
	w.writeByte(list)
	w.writeInt32(int32(action))
	return w.bytes()
}

// BuildStackableActionFailure includes the fields that RVA 0x20f425a reads
// after the dispatcher consumes result/error. Its action field is one byte.
func BuildStackableActionFailure(slot int16, list, action, code byte) []byte {
	var w packetWriter
	w.writeByte(0)
	w.writeByte(code)
	w.writeInt16(slot)
	w.writeByte(list)
	w.writeByte(action)
	return w.bytes()
}
