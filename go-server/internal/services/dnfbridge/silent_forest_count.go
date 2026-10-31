package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Current client receiver 0x2008340 reads a u32 row count followed by
// (u32 dungeon, u8 remaining). The selector hides missing cache rows (-1).
// This notification requires the paired client receive-gate update.
const currentAccountDungeonInoutCountMsgID uint16 = 1203

func buildCurrentSilentForestCountBody(remaining byte) []byte {
	body := make([]byte, 9)
	binary.LittleEndian.PutUint32(body, 1)
	binary.LittleEndian.PutUint32(body[4:], uint32(dnfdungeon.SilentForestDungeonID))
	body[8] = remaining
	return body
}

func (s *Service) sendCurrentSilentForestCount(session *gameSession, character dnfrepo.CharacterRecord) error {
	if !s.currentEpicEntryEnabled(dnfdungeon.SilentForestDungeonID) || s.initialEquipmentArchive == nil {
		return nil
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok || character.CharacterID != strconv.Itoa(int(identity.character)) || character.AccountID != s.accountIDForSession(session) {
		return dnfdungeon.ErrOwnerUnavailable
	}
	limit, err := s.currentSilentForestEntryLimit()
	if err != nil {
		return err
	}
	g, ok := s.repositoryGroup()
	if !ok || g.Account == nil {
		return dnfdungeon.ErrOwnerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	account, found, err := g.Account.Load(ctx, character.AccountID)
	if err != nil {
		return err
	}
	if !found || account.AccountID != character.AccountID {
		return dnfdungeon.ErrAccountNotFound
	}
	used, err := dnfdungeon.SilentForestEntryUsed(account, s.gameplayNow())
	if err != nil {
		return err
	}
	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return dnfdungeon.ErrOwnerUnavailable
	}
	return s.sendGameUpperRawClass(session, currentAccountDungeonInoutCountMsgID, buildCurrentSilentForestCountBody(byte(remaining)), 0)
}
