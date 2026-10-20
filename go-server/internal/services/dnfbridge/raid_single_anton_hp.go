package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/raid"
)

// SingleIndex is advanced only by the owned completed-settlement reducer.
// Cross-check its journal before projecting cumulative clears. Map state 1
// alone is insufficient: both future maps and cleared maps carry that state.
func currentSingleAntonHPBody(b *runtimeRaidBattle) ([]byte, error) {
	if b == nil || !b.Single || b.Mode != 1 || b.Phase != 0 || b.Anton != nil ||
		len(b.SingleHPSymbols) == 0 || len(b.MemberAccounts) != 1 || len(b.SingleSequence) == 0 ||
		b.SingleIndex < 0 || b.SingleIndex > len(b.SingleSequence) || len(b.ClearedRuns) != b.SingleIndex {
		return nil, fmt.Errorf("single Anton HP owner unavailable")
	}
	var member uint16
	for id, account := range b.MemberAccounts {
		if id == 0 || account == "" || b.MemberClears[id] != uint32(b.SingleIndex) {
			return nil, fmt.Errorf("single Anton HP member/journal mismatch")
		}
		member = id
	}
	values := make(map[uint32]uint32, len(b.SingleHPSymbols))
	for dungeon, symbol := range b.SingleHPSymbols {
		if dungeon == 0 || symbol == 0 {
			return nil, fmt.Errorf("single Anton HP binding invalid")
		}
		if _, exists := values[symbol]; exists {
			return nil, fmt.Errorf("single Anton HP binding repeated")
		}
		values[symbol] = 0
	}
	prefix, seen := map[uint32]bool{}, map[uint32]bool{}
	for i, dungeon := range b.SingleSequence {
		if seen[dungeon] || b.SingleHPSymbols[dungeon] == 0 {
			return nil, fmt.Errorf("single Anton HP weekly sequence mismatch")
		}
		seen[dungeon] = true
		if i < b.SingleIndex {
			prefix[dungeon] = true
		}
	}
	for runID, run := range b.ClearedRuns {
		if runID == "" || !prefix[run.DungeonID] || run.Phase != 0 || run.Attempt != 1 ||
			len(run.Participants) != 1 || run.Participants[0] != member {
			return nil, fmt.Errorf("single Anton HP clear journal mismatch")
		}
		delete(prefix, run.DungeonID)
		values[b.SingleHPSymbols[run.DungeonID]] = 1
	}
	if len(prefix) != 0 {
		return nil, fmt.Errorf("single Anton HP clear prefix incomplete")
	}
	return raid.BuildSymbolBody(values)
}
