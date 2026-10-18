// Package adminapi defines the private game-runtime management contract.
// It contains no web panel, database connection, scheduler or game transport.
package adminapi

import (
	"longheng.io/server/internal/modules/dnf/onlineevent"
	"longheng.io/server/internal/modules/dnf/repository"
)

const Version = 1
const Path = "/api/game/admin"

type Request struct {
	Version     int                         `json:"version"`
	Action      string                      `json:"action"`
	CharacterID uint16                      `json:"characterId,omitempty"`
	Revision    uint64                      `json:"revision,omitempty"`
	Changes     []onlineevent.ControlChange `json:"changes,omitempty"`
	ListType    byte                        `json:"listType,omitempty"`
	Slot        int16                       `json:"slot,omitempty"`
	Stack       *repository.ItemStack       `json:"stack,omitempty"`
}

type Response struct {
	Version            int                          `json:"version"`
	OK                 bool                         `json:"ok"`
	Error              string                       `json:"error,omitempty"`
	Online             bool                         `json:"online"`
	Activities         *onlineevent.ControlSnapshot `json:"activities,omitempty"`
	PremiumTypes       []int64                      `json:"premiumTypes,omitempty"`
	CrystalCubeItemIDs [6]int64                     `json:"crystalCubeItemIds,omitempty"`
	StackRow           []byte                       `json:"stackRow,omitempty"`
}
