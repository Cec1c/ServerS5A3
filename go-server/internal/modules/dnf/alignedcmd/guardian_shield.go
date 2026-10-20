package alignedcmd

import "context"

// GuardianShieldResolution is the active-PVF proof for one item admitted to
// the Guardian's five-slot shield deck. The bridge resolves archive metadata;
// the domain owner persists only this typed result inside its character-asset
// transaction.
type GuardianShieldResolution struct {
	ItemID        uint32
	PVFPath       string
	EquipmentType string
	UnlockKind    GuardianShieldUnlockKind
	ClearQuestID  int64
	MinimumLevel  int64
}

type GuardianShieldUnlockKind byte

const (
	GuardianShieldUnlockUnknown GuardianShieldUnlockKind = iota
	GuardianShieldUnlockQuest
	GuardianShieldUnlockLevel
)

// GuardianShieldResolver validates shieldwindownewdata membership and its
// quest/level authorization for the character's normalized grow type together
// with the concrete equipment file ([support weapon], usable knight, shield
// group and shield path).
type GuardianShieldResolver func(context.Context, uint32, byte) (GuardianShieldResolution, error)
