package dnfbridge

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// Current op14 resolves list12 to the account-cargo object, and consumes the
// same 119-byte rows/tombstones as list0/2. See ACCOUNT_CARGO_SORT.md. Freeze
// the durable endpoints instead of reloading and replacing every bag object
// for each item in the client's serial bulk-transfer queue.
func (s *Service) sendCommittedAccountCargoMove(session *gameSession, projection *alignedcmd.AccountCargoMoveProjection) error {
	if projection == nil {
		return nil
	}
	if session == nil || session.selectedCharacterID == 0 || projection.CharacterID != strconv.Itoa(int(session.selectedCharacterID)) || projection.AccountID != s.accountIDForSession(session) {
		return fmt.Errorf("committed account cargo projection owner mismatch")
	}
	if len(projection.Rows) != 2 {
		return fmt.Errorf("committed account cargo projection needs two endpoints")
	}
	entries := make(map[byte][]currentItemListEntry)
	var order []byte
	for _, row := range projection.Rows {
		if (row.ListType != 0 && row.ListType != 2 && row.ListType != 12) || row.SlotIndex < 0 {
			return fmt.Errorf("invalid cargo projection endpoint")
		}
		if _, found := entries[row.ListType]; !found {
			order = append(order, row.ListType)
		}
		var entry currentItemListEntry
		if row.Stack == nil {
			entry.patchCore(row.SlotIndex, math.MaxUint32, 0)
		} else {
			entry = currentItemListEntryFromStack(row.ListType, row.SlotIndex, *row.Stack)
		}
		entries[row.ListType] = append(entries[row.ListType], entry)
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	// Prepare both packets before sending either. These project only frozen
	// rows; no inventory, account, settings or wallet repository reads occur.
	bodies := make(map[byte][]byte)
	for _, list := range order {
		if _, err := s.applyCurrentPVFUsePeriodsToEntriesWithLoadedCatalog(ctx, entries[list]); err != nil {
			s.logPacketEvent("account-cargo-move-use-period-projection-fallback", "char_id", session.selectedCharacterID, "err", err)
		}
		bodies[list] = buildCurrentItemUpdateBody(list, entries[list])
	}
	for _, list := range order {
		if err := s.sendGameUpperRawClass(session, 14, bodies[list], 0); err != nil {
			return err
		}
	}
	s.logPacketEvent("account-cargo-move-endpoint-refresh", "conn_id", session.connID, "char_id", session.selectedCharacterID, "row_count", len(projection.Rows), "container_count", len(order))
	return nil
}
