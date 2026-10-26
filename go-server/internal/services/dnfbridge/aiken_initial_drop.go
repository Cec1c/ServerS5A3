package dnfbridge

import (
	"crypto/rand"
	"encoding/binary"
	"math/big"

	aiken "longheng.io/server/internal/modules/dnf/aikenupgrade"
)

func currentAikenInitialRoll(bound uint32) (uint32, error) {
	if bound == 0 {
		return 0, aiken.ErrInvalid
	}
	n, err := rand.Int(rand.Reader, new(big.Int).SetUint64(uint64(bound)))
	if err != nil {
		return 0, err
	}
	return uint32(n.Uint64()), nil
}

func currentAikenInitialDropState(definition dungeonDropItemDefinition) ([5]byte, error) {
	if definition.AikenInitial == nil {
		return [5]byte{}, nil
	}
	state := definition.AikenInitialState
	if state == [5]byte{} {
		return definition.AikenInitial.Roll(currentAikenInitialRoll)
	}
	if !definition.AikenInitial.Valid(state) {
		return [5]byte{}, aiken.ErrInvalid
	}
	return state, nil
}

// Ground creation freezes the roll before publishing/registering the drop.
// Pickup and transport retries consume this value without another draw.
func freezeCurrentAikenInitialDrop(definition *dungeonDropItemDefinition, entry currentItemListEntry) {
	if definition.AikenInitial != nil {
		copy(definition.AikenInitialState[:], entry.data[0x72:0x77])
	}
}

func initializeCurrentAikenPassiveDrop(drop *runtimeDungeonDrop) error {
	if drop.Item.AikenInitial == nil {
		return nil
	}
	entry, err := currentDungeonDeathDropItemState(drop.SceneSlot, drop.Item, drop.Amount, drop.QualitySeed)
	if err != nil {
		return err
	}
	freezeCurrentAikenInitialDrop(&drop.Item, entry)
	drop.QualitySeed = binary.LittleEndian.Uint32(entry.data[6:10])
	return nil
}
