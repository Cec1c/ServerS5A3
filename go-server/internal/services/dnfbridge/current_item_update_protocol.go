package dnfbridge

import "math"

func buildCurrentGoldStateBody(gold int64) []byte {
	if gold < 0 {
		gold = 0
	}
	if gold > math.MaxInt32 {
		gold = math.MaxInt32
	}
	var entry currentItemListEntry
	entry.patchCore(0, 0, uint32(gold))
	return buildCurrentItemUpdateBody(0, []currentItemListEntry{entry})
}

func buildCurrentItemUpdateBody(listType byte, entries []currentItemListEntry) []byte {
	var body packetWriter
	body.writeByte(listType)
	body.writeUint16(uint16(len(entries)))
	for _, entry := range entries {
		body.writeBytes(entry.data[:])
		if listType == 1 || (listType == 3 && entry.list3WornAvatar) {
			// List-1, and current-EXE list-3 worn actor slots 0..11, are not
			// ordinary raw item rows. Their readers consume two length-prefixed
			// avatar extension blobs after each 0x77-byte item record. The first
			// blob carries avatar socket data after open/embed mutations;
			// omitting both length fields shifts the next packet reader.
			writeCurrentAvatarItemListOptionalBlobs(&body, entry)
		}
		// Current sub_225C960/sub_225C9B0 read list-3 blobs only when the
		// item-table type is <= 11. Do not append them to gate-false rows at
		// actor slots 12..32: extra length fields would become bytes of the
		// following packet.
	}
	return body.bytes()
}
