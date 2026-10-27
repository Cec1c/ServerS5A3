package dnfbridge

import (
	"context"
	"fmt"
	"strconv"

	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/raidcurrency"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const currentRaidCurrencyListType byte = 44

func currentAntonPlanContainsFate(plan currentAntonRewardPlan) bool {
	for _, reward := range plan.Rewards {
		if reward.Definition.ItemID == raidcurrency.FateItemID {
			return true
		}
	}
	return false
}

func (s *Service) refreshCurrentFateSnapshot(session *gameSession) error {
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	return s.sendCurrentFateSnapshot(ctx, session, false)
}

// Reward-flow recovery uses only committed account state; no Credit is called.
func (s *Service) refreshCurrentRaidFateSnapshot(session *gameSession) error {
	if !s.currentRaidAuthenticated(session) {
		return raid.ErrRewardOwnership
	}
	return s.refreshCurrentFateSnapshot(session)
}

// The account ledger is authoritative. V179's shop reads the item count from
// list44; opcode1456 is a separate earned/limit presentation. Both V179 raw
// and protected readers accept list44 as u8 list, u16 rows, raw[0x77] rows.
func currentFateItemRow(balance uint32) currentItemListEntry {
	var row currentItemListEntry
	row.patchCore(0, raidcurrency.FateItemID, balance)
	return row
}

func buildCurrentFateWeeklyBody(state raidcurrency.State) []byte {
	var body packetWriter
	body.writeUint16(1)
	body.writeUint32(raidcurrency.FateItemID)
	body.writeUint32(state.Earned)
	body.writeUint32(raidcurrency.WeeklyLimit)
	return body.bytes()
}

func (s *Service) currentFateState(ctx context.Context, session *gameSession) (raidcurrency.State, bool, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return raidcurrency.State{}, false, nil
	}
	g, ok := s.repositoryGroup()
	if !ok || g.Character == nil || g.Account == nil {
		return raidcurrency.State{}, false, fmt.Errorf("raid currency repositories unavailable")
	}
	accountID := s.accountIDForSession(session)
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	character, found, err := g.Character.Load(ctx, characterID)
	if err != nil {
		return raidcurrency.State{}, false, err
	}
	if !found || character.CharacterID != characterID || character.AccountID != accountID {
		return raidcurrency.State{}, false, fmt.Errorf("raid currency owner mismatch")
	}
	account, found, err := g.Account.Load(ctx, accountID)
	if err != nil {
		return raidcurrency.State{}, false, err
	}
	if !found || account.AccountID != accountID {
		return raidcurrency.State{}, false, fmt.Errorf("raid currency account unavailable")
	}
	state, err := raidcurrency.Read(account, s.gameplayNow())
	return state, true, err
}

func (s *Service) sendCurrentFateSnapshot(ctx context.Context, session *gameSession, full bool) error {
	state, known, err := s.currentFateState(ctx, session)
	if err != nil || !known {
		return err
	}
	return s.sendPreparedCurrentFateSnapshot(session, state, full)
}

// Bootstrap prepares state before its first container packet. Keep transport
// separate so no later account read can fail after installing half a snapshot.
func (s *Service) sendPreparedCurrentFateSnapshot(session *gameSession, state raidcurrency.State, full bool) error {
	rows := []currentItemListEntry{currentFateItemRow(state.Balance)}
	op := uint16(14)
	body := buildCurrentItemUpdateBody(currentRaidCurrencyListType, rows)
	if full {
		op = 13
		body = buildCurrentItemListBody(currentRaidCurrencyListType, rows, repo.CharacterContainerState{})
	}
	if err := s.sendGameUpperRawClass(session, op, body, 0); err != nil {
		return err
	}
	return s.sendGameUpperRawClass(session, 1456, buildCurrentFateWeeklyBody(state), 0)
}

func (s *Service) migrateCurrentFateAccount(ctx context.Context, session *gameSession) error {
	g, ok := s.repositoryGroup()
	if !ok {
		return fmt.Errorf("raid currency repositories unavailable")
	}
	_, err := raidcurrency.MigrateAccount(ctx, g, s.accountIDForSession(session), s.gameplayNow())
	return err
}
