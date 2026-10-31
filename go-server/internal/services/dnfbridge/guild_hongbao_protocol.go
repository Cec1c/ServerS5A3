package dnfbridge

import (
	"encoding/binary"
	"fmt"
)

const (
	currentGuildHongbaoHistoryOpcode uint16 = 1302
	currentGuildHongbaoIssueOpcode   uint16 = 1303
	currentGuildHongbaoClaimOpcode   uint16 = 1304
)

type currentGuildHongbaoRow struct {
	ID, ItemID uint32
	Name       string
	Portions   byte
	Completed  bool
	Claimed    bool // This account claimed; other accounts can still take a share.
}

type currentGuildHongbaoClaimRow struct {
	Level  byte
	Name   string
	Amount uint32
}

func parseCurrentGuildHongbaoIssue(body []byte) (uint32, byte, error) {
	if len(body) != 5 || binary.LittleEndian.Uint32(body) == 0 || body[4] < 5 || body[4] > 30 {
		return 0, 0, fmt.Errorf("invalid hongbao issue request")
	}
	return binary.LittleEndian.Uint32(body), body[4], nil
}

func parseCurrentGuildHongbaoID(body []byte) (uint32, error) {
	if len(body) != 4 || binary.LittleEndian.Uint32(body) == 0 {
		return 0, fmt.Errorf("invalid hongbao record request")
	}
	return binary.LittleEndian.Uint32(body), nil
}

func buildCurrentGuildHongbaoListBody(memberCount uint16, spent, claims uint32, rows []currentGuildHongbaoRow) ([]byte, error) {
	if len(rows) > 500 {
		return nil, fmt.Errorf("hongbao list exceeds native buffer")
	}
	var w packetWriter
	w.writeUint16(memberCount)
	w.writeUint32(spent)
	w.writeUint32(claims)
	w.writeUint32(uint32(len(rows)))
	for _, row := range rows {
		name := rosterNameBytes(row.Name)
		if row.ID == 0 || row.ItemID == 0 || len(name) > 29 || row.Portions > 30 {
			return nil, fmt.Errorf("invalid hongbao list row")
		}
		var wire [40]byte
		binary.LittleEndian.PutUint32(wire[:4], row.ID)
		binary.LittleEndian.PutUint32(wire[4:8], row.ItemID)
		copy(wire[8:38], name)
		wire[38] = row.Portions
		if row.Completed {
			wire[39] = 1
		} else if row.Claimed {
			wire[39] = 2
		}
		w.writeBytes(wire[:])
	}
	return w.bytes(), nil
}

func buildCurrentGuildHongbaoHistoryBody(rows []currentGuildHongbaoClaimRow) ([]byte, error) {
	if len(rows) > 30 {
		return nil, fmt.Errorf("hongbao history exceeds native buffer")
	}
	var w packetWriter
	w.writeUint32(uint32(len(rows)))
	for _, row := range rows {
		name := rosterNameBytes(row.Name)
		if len(name) > 29 || row.Amount == 0 {
			return nil, fmt.Errorf("invalid hongbao history row")
		}
		var wire [36]byte
		wire[0] = row.Level
		copy(wire[1:31], name)
		binary.LittleEndian.PutUint32(wire[31:35], row.Amount)
		// The native constructor initializes the final marker to zero.
		w.writeBytes(wire[:])
	}
	return w.bytes(), nil
}

func buildCurrentGuildHongbaoIssueBody(itemID, recordID uint32) []byte {
	var w packetWriter
	w.writeUint32(0)
	w.writeUint32(itemID)
	w.writeUint32(recordID) // The list uses model+68 to animate the new row.
	return w.bytes()
}

func buildCurrentGuildHongbaoClaimBody(amount uint32) []byte {
	var w packetWriter
	w.writeUint32(0)
	w.writeUint32(0) // Reserved native result field, not an account balance.
	w.writeUint32(amount)
	return w.bytes()
}
