package dnfbridge

import (
	"encoding/binary"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

const currentWeddingActorRawOffset = 0x17

// The common mode0 producer is used for the selected actor, town/dungeon
// peers, and later appearance refreshes. Keep every carrier on the same
// persisted marriage snapshot instead of clearing the native rebuild cache.
func writeCurrentWeddingActorRawState(raw []byte, character r.CharacterRecord, present bool) {
	if !present || len(raw) < currentWeddingActorRawOffset+6 {
		return
	}
	state, err := wedding.Read(character)
	if err != nil || state.Ring > 5 {
		// The paired PVF and native animation table have ring indices0..5.
		// Invalid records must not grant attributes or index missing resources.
		return
	}
	raw[currentWeddingActorRawOffset] = state.Phase
	if state.Phase == wedding.Married {
		raw[currentWeddingActorRawOffset+1] = byte(state.Ring)
		binary.LittleEndian.PutUint32(raw[currentWeddingActorRawOffset+2:], uint32(state.RoomPoints))
	}
}
