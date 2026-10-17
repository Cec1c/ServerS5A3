package dnfbridge

import (
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The 24.9.12.0 EXE mode-2 reader at 0x022c78a0 reads two reserved
// bytes, job, packed grow/awakening, level and two state bytes before its two u32
// timestamps. Its post-equipment readers consume 42 bytes, including the
// four final flags in 0x022bea20. The older reader consumes only 38.
// Omitting those four flags makes the next character start four bytes late.
func writeOfficial249RosterEntry(writer *packetWriter, index int, character dnfrepo.CharacterRecord) {
	writeOfficial249RosterEntryWithPermission(writer, index, character, dnfquest.SpecialChannelPermission{})
}

func writeOfficial249RosterEntryWithPermission(writer *packetWriter, index int, character dnfrepo.CharacterRecord, permission dnfquest.SpecialChannelPermission) {
	writer.writeUint16(uint16(rosterSlotValue(character.Slot, index)))
	writer.writeRawDstr(rosterRawNameBytes(character))
	if currentCharacterRenamePending(character) {
		writer.writeByte(1)
	} else {
		writer.writeByte(0)
	}
	writer.writeByte(0)
	writer.writeByte(byte(numericCharacterStat(character.Job)))
	writer.writeByte(characterGrowTypeByte(character))
	writer.writeByte(rosterLevel(character))
	writer.writeZero(2)
	writer.writeUint32(0)
	writer.writeUint32(0)
	writeNoPackRosterEquipSummary(writer, characterEquipSummary(character))
	writer.writeZero(24)
	writer.writeByte(3)
	writer.writeByte(0)
	writer.writeByte(0)
	writer.writeByte(4)
	writer.writeZero(10)
	// Current 022BEA20 reads four final u8 flags into selector +449/+44A/
	// +44B/+44D. 036D37CD tests +449 for Anton type 23 and +44A for Luke
	// type 32. 036D38C2 tests +44D for Fiend War type 45; this is the
	// fourth stream byte, not the intervening Beast permission at +44B.
	for _, allowed := range []bool{permission.Anton, permission.Luke, false, permission.FiendWar} {
		flag := byte(0)
		if allowed {
			flag = 1
		}
		writer.writeByte(flag)
	}
}
