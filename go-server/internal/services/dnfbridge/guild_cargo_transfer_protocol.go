package dnfbridge

import (
	"encoding/binary"
	"fmt"

	inventory "longheng.io/server/internal/modules/dnf/inventory"
)

// Matched v105/V107 native247 sender VA 0x25f28a0, receiver 0x1fd3d60.
// Decode only the fixed wire layout. The eventual handler must bind the
// authenticated owner, current guild and session guard before the transaction.
func parseCurrentGuildCargoDepositRequest(body []byte) (inventory.GuildCargoDepositRequest, bool) {
	if len(body) != 13 {
		return inventory.GuildCargoDepositRequest{}, false
	}
	return inventory.GuildCargoDepositRequest{
		SourceList: body[0], SourceSlot: binary.LittleEndian.Uint16(body[1:3]),
		ItemID: binary.LittleEndian.Uint32(body[3:7]), Count: binary.LittleEndian.Uint32(body[7:11]),
		CargoSlot: binary.LittleEndian.Uint16(body[11:13]),
	}, true
}

// Native248 sender VA 0x25f2bf0 has no destination-slot field. Its callback
// 0x1fc9d40 consumes the destination actually allocated by the transaction.
func parseCurrentGuildCargoWithdrawalRequest(body []byte) (inventory.GuildCargoWithdrawalRequest, bool) {
	if len(body) != 11 {
		return inventory.GuildCargoWithdrawalRequest{}, false
	}
	return inventory.GuildCargoWithdrawalRequest{
		CargoSlot: binary.LittleEndian.Uint16(body[:2]), ItemID: binary.LittleEndian.Uint32(body[2:6]),
		Count: binary.LittleEndian.Uint32(body[6:10]), DestinationList: body[10],
	}, true
}

// Both payloads exclude the dispatcher success byte. They must only be sent
// after commit, before authoritative inventory/cargo refreshes: the native
// callbacks first move existing client objects using these nine bytes.
func buildCurrentGuildCargoDepositSuccessBody(req inventory.GuildCargoDepositRequest, result inventory.GuildCargoResult) ([]byte, error) {
	if req.GuildID == 0 || result.GuildID != req.GuildID || req.Count == 0 || result.Moved != req.Count ||
		result.Destination != (inventory.GuildCargoLocation{Cargo: true, Slot: req.CargoSlot}) {
		return nil, fmt.Errorf("guild cargo deposit result does not match request")
	}
	body := make([]byte, 9)
	body[0] = req.SourceList
	binary.LittleEndian.PutUint16(body[1:3], req.SourceSlot)
	binary.LittleEndian.PutUint32(body[3:7], result.Moved)
	binary.LittleEndian.PutUint16(body[7:9], result.Destination.Slot)
	return body, nil
}

func buildCurrentGuildCargoWithdrawalSuccessBody(req inventory.GuildCargoWithdrawalRequest, result inventory.GuildCargoResult) ([]byte, error) {
	if req.GuildID == 0 || result.GuildID != req.GuildID || req.Count == 0 || result.Moved != req.Count ||
		result.Destination.Cargo || result.Destination.List != req.DestinationList {
		return nil, fmt.Errorf("guild cargo withdrawal result does not match request")
	}
	body := make([]byte, 9)
	binary.LittleEndian.PutUint16(body[:2], req.CargoSlot)
	body[2] = result.Destination.List
	binary.LittleEndian.PutUint16(body[3:5], result.Destination.Slot)
	binary.LittleEndian.PutUint32(body[5:9], result.Moved)
	return body, nil
}
