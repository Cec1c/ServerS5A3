package dnfbridge

import (
	"context"
	"fmt"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const currentDungeonAccountIncountPath = "etc/dungeonetc/dungeonincountinfo.etc"

func (s *Service) currentSilentForestEntryLimit() (int64, error) {
	if s == nil || s.initialEquipmentArchive == nil {
		return 0, dnfdungeon.ErrOwnerUnavailable
	}
	text, err := s.initialEquipmentArchive.ReadText(currentDungeonAccountIncountPath)
	if err != nil {
		return 0, err
	}
	doc, err := dnfpvf.Parse(currentDungeonAccountIncountPath, text)
	if err != nil {
		return 0, err
	}
	rows := doc.Ints("account dungeon incount info")
	if len(rows)%4 != 0 {
		return 0, fmt.Errorf("invalid account dungeon incount rows")
	}
	var limit int64
	for i := 0; i < len(rows); i += 4 {
		if rows[i+1] != dnfdungeon.SilentForestDungeonID {
			continue
		}
		if limit != 0 || rows[i] != 0 || rows[i+2] != 10 || rows[i+3] != 0 {
			return 0, fmt.Errorf("silent forest account admission PVF contract changed")
		}
		limit = rows[i+2]
	}
	if limit == 0 {
		return 0, fmt.Errorf("silent forest account admission PVF rule missing")
	}
	return limit, nil
}

func (s *Service) validateCurrentSilentForestEntry(ctx context.Context, character dnfrepo.CharacterRecord, dungeon worldmap.Dungeon, session *gameSession) error {
	if err := validateCurrentEpicEntryContract(dungeon); err != nil {
		return err
	}
	limit, err := s.currentSilentForestEntryLimit()
	if err != nil {
		return err
	}
	g, ok := s.repositoryGroup()
	if !ok || g.Account == nil {
		return dnfdungeon.ErrOwnerUnavailable
	}
	if character.AccountID == "" || character.AccountID != s.accountIDForSession(session) {
		return errDungeonAccountMismatch
	}
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
	if used >= limit {
		return dnfdungeon.ErrDailyEntryLimit
	}
	return validateDungeonEntry(character, dungeon, session)
}
