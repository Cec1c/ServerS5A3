package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentDungeonCycleInfoMsgID uint16 = 656

// Current 2032716 registers NOTI656 -> 2007c00. It clears the whole cache,
// then reads u8 count and four u32 per row. 1f61b40 aggregates A/C by Order;
// 158d7b9/158d831 use C for remaining allowances and A for cycle progress.
func (s *Service) currentDungeonCycleRows() ([]currentDungeonCycleRow, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	s.initialEquipmentMu.Lock()
	a, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		if strings.TrimSpace(s.options.pvfPath) == "" {
			return nil, false, nil
		}
		return nil, false, err
	}
	if a == nil {
		return nil, false, nil
	}
	text, err := a.ReadText(currentDungeonCycleConfigPath)
	if err != nil {
		return nil, false, err
	}
	return parseCurrentDungeonCycleRows(text)
}

func cycleAdmissionForRows(rows []currentDungeonCycleRow, id int64) (dnfdungeon.CycleEntryAllowance, bool, error) {
	rule := dnfdungeon.CycleEntryAllowance{}
	var target *currentDungeonCycleRow
	for i := range rows {
		if rows[i].DungeonID == id {
			target = &rows[i]
		}
	}
	if target == nil {
		return rule, false, nil
	}
	if target.Order < 0 || target.Order > 1 || target.DailyCount <= 0 || target.DailyCount > math.MaxInt32 {
		return rule, true, fmt.Errorf("unsupported cycle order or limit")
	}
	if target.Order == 1 && target.DailyCount != 1 {
		return rule, true, fmt.Errorf("current native trial UI requires limit1")
	}
	rule.Limit = target.DailyCount
	for _, row := range rows {
		if row.Order == target.Order {
			if row.DailyCount != target.DailyCount {
				return rule, true, fmt.Errorf("inconsistent cycle group limits")
			}
			rule.DungeonIDs = append(rule.DungeonIDs, row.DungeonID)
		}
		if target.Order > 0 && row.Order == 0 {
			if rule.PrerequisiteCount != 0 && rule.PrerequisiteCount != row.DailyCount {
				return rule, true, fmt.Errorf("inconsistent cycle progress limits")
			}
			rule.PrerequisiteCount = row.DailyCount
			rule.PrerequisiteDungeonIDs = append(rule.PrerequisiteDungeonIDs, row.DungeonID)
		}
	}
	if target.Order > 0 && len(rule.PrerequisiteDungeonIDs) == 0 {
		return rule, true, fmt.Errorf("cycle trial prerequisite missing")
	}
	sort.Slice(rule.DungeonIDs, func(i, j int) bool { return rule.DungeonIDs[i] < rule.DungeonIDs[j] })
	sort.Slice(rule.PrerequisiteDungeonIDs, func(i, j int) bool { return rule.PrerequisiteDungeonIDs[i] < rule.PrerequisiteDungeonIDs[j] })
	return rule, true, nil
}

func (s *Service) currentCycleAdmission(id int64) (dnfdungeon.CycleEntryAllowance, bool, error) {
	rows, found, err := s.currentDungeonCycleRows()
	if err != nil || !found {
		return dnfdungeon.CycleEntryAllowance{}, found, err
	}
	return cycleAdmissionForRows(rows, id)
}

func (s *Service) buildCurrentDungeonCycleInfo(character dnfrepo.CharacterRecord) ([]byte, bool, error) {
	rows, found, err := s.currentDungeonCycleRows()
	if err != nil || !found {
		return nil, false, err
	}
	if len(rows) > 255 {
		return nil, false, fmt.Errorf("cycle count exceeds native u8")
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].DungeonID < rows[j].DungeonID })
	body := make([]byte, 1+16*len(rows))
	body[0] = byte(len(rows))
	at := s.gameplayNow()
	for i, row := range rows {
		if row.DungeonID <= 0 || row.DungeonID > math.MaxUint32 {
			return nil, false, fmt.Errorf("invalid cycle dungeon id")
		}
		if _, _, err := cycleAdmissionForRows(rows, row.DungeonID); err != nil {
			return nil, false, err
		}
		A, err := dnfdungeon.DailyEntryUsed(character, row.DungeonID, at)
		if err != nil {
			return nil, false, err
		}
		C, err := dnfdungeon.CycleEntryUsed(character, row.DungeonID, at)
		if err != nil {
			return nil, false, err
		}
		if A > row.DailyCount {
			A = row.DailyCount
		}
		if C > row.DailyCount {
			C = row.DailyCount
		}
		off := 1 + 16*i
		binary.LittleEndian.PutUint32(body[off:], uint32(row.DungeonID))
		binary.LittleEndian.PutUint32(body[off+4:], uint32(A))
		// B is unused by the observed current aggregate/UI, defaulting to zero.
		binary.LittleEndian.PutUint32(body[off+12:], uint32(C))
	}
	return body, true, nil
}

func (s *Service) sendCurrentDungeonCycleInfo(session *gameSession, character dnfrepo.CharacterRecord) error {
	if s == nil || s.options.currentClientVersion != "official-24.9" || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	if character.CharacterID != strconv.Itoa(int(session.selectedCharacterID)) {
		return fmt.Errorf("cycle projection character changed")
	}
	if strings.TrimSpace(character.AccountID) != "" && strings.TrimSpace(character.AccountID) != strings.TrimSpace(s.accountIDForSession(session)) {
		return fmt.Errorf("cycle projection owner changed")
	}
	body, found, err := s.buildCurrentDungeonCycleInfo(character)
	if err != nil || !found {
		return err
	}
	return s.sendGameUpperRawClass(session, currentDungeonCycleInfoMsgID, body, 0)
}

func (s *Service) refreshCurrentDungeonCycleInfo(session *gameSession, runtime *runtimeDungeonState, source string) {
	if runtime == nil {
		return
	}
	_, found, err := s.currentCycleAdmission(runtime.Dungeon.ID)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-cycle-projection-deferred", "source", source, "error", err)
		return
	}
	if !found {
		return
	}
	if err := s.sendCurrentDungeonCycleInfo(session, runtime.Character); err != nil {
		s.logGameEvent(session, "game-dungeon-cycle-projection-deferred", "source", source, "error", err)
	}
}
