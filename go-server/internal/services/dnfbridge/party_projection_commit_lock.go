package dnfbridge

import (
	"errors"
	"sync"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

var errCurrentPartyCommitBusy = errors.New("party projection commit lock busy")

// manager 回调内不等待 bridge/session 锁；等待前退出整个 guard，重试时重新验证成员代际。
// apply 只允许原子校验和内存写入，不得发包、记日志或重新调用 manager。
func (s *Service) withCurrentPartyProjectionCommit(guard func(func() error) error, projection *sync.Mutex, apply func() error) error {
	for {
		var blocked *sync.Mutex
		err := guard(func() error {
			if !s.mu.TryLock() {
				blocked = &s.mu
				return errCurrentPartyCommitBusy
			}
			defer s.mu.Unlock()
			if !projection.TryLock() {
				blocked = projection
				return errCurrentPartyCommitBusy
			}
			defer projection.Unlock()
			return apply()
		})
		if err != errCurrentPartyCommitBusy {
			return err
		}
		// 此处 manager、s.mu 和 projection 均已释放；不把忙碌当作提交成功。
		blocked.Lock()
		blocked.Unlock()
	}
}

// 取消阶段与绑定提交之间可能让出 dungeon 锁，提交前必须核对运行对象、token 和计时器代际。
type currentPartyBindingDungeonSnapshot struct {
	runtime        *runtimeDungeonState
	runToken       uint64
	lifecycleToken uint64
	generations    [4]uint64
	names          [4]string
}

func currentPartyBindingDungeonSnapshotLocked(session *gameSession) currentPartyBindingDungeonSnapshot {
	result := currentPartyBindingDungeonSnapshot{runtime: session.dungeon.runtime, runToken: session.dungeon.runToken}
	if r := result.runtime; r != nil {
		result.lifecycleToken = r.lifecycleToken
		result.generations = [4]uint64{r.deathReturnGeneration, r.crystalContractTimerGeneration, r.settlementCardAutoFlipGeneration, r.tournamentRewardAutoSelectGeneration}
		result.names = [4]string{r.deathReturnTimerName, r.crystalContractTimerName, r.settlementCardAutoFlipTimerName, r.tournamentRewardAutoSelectTimerName}
	}
	return result
}

// 返回时持有完整绑定提交锁；调用方完成纯内存更新后必须 unlock，再做 rebind/广播。
// 预约、resident、runtime 或锁竞争导致重试时，等待点不持有任何提交锁。
func (s *Service) lockCurrentPartyBinding(session *gameSession, charID uint16) (*gameSession, alignedcmd.PartyState, func(), bool) {
	s.mu.Lock()
	expected := boundGameSessionCharacter{session: session, character: session.selectedCharacterID, generation: session.characterGeneration}
	s.mu.Unlock()
	if expected.character != charID || expected.generation == 0 {
		s.markCurrentRaidSessionRestore(session, charID)
	}
	var cancelled currentPartyBindingDungeonSnapshot
	var cancelledValid bool
	var settledResident boundGameSessionCharacter
	for {
		var resident *gameSession
		if s.onlinePlayers != nil {
			resident = s.onlinePlayers.SessionForCharacter(charID)
		}
		s.mu.Lock()
		if session.selectedCharacterID != expected.character || session.characterGeneration != expected.generation {
			s.mu.Unlock()
			return nil, alignedcmd.PartyState{}, nil, false
		}
		residentIdentity := boundGameSessionCharacter{session: resident}
		if resident != nil {
			residentIdentity.character, residentIdentity.generation = resident.selectedCharacterID, resident.characterGeneration
		}
		reconnect := session.channelReconnect
		s.mu.Unlock()
		if resident != nil && resident != session && reconnect && residentIdentity != settledResident {
			// 保留 op13 背包前同步结算旧连接时钟的顺序；换了 resident 必须重新结算。
			s.stopCurrentSpendTimeClock(resident, "replacement_game_session_bound_before_inventory_bootstrap")
			s.stopCurrentDailyDeleteClock(resident)
			settledResident = residentIdentity
		}

		session.dungeon.mu.Lock()
		if !s.mu.TryLock() {
			session.dungeon.mu.Unlock()
			s.mu.Lock()
			s.mu.Unlock()
			continue
		}
		identityCurrent := session.selectedCharacterID == expected.character && session.characterGeneration == expected.generation
		s.mu.Unlock()
		if !identityCurrent {
			session.dungeon.mu.Unlock()
			return nil, alignedcmd.PartyState{}, nil, false
		}
		prepared := currentPartyBindingDungeonSnapshotLocked(session)
		if expected.character != 0 && expected.character != charID && (!cancelledValid || prepared != cancelled) {
			// 取消可能记日志，不能放到持 s.mu 的提交区。
			s.cancelCurrentDungeonDeathReturnLocked(session, prepared.runtime, "selected_character_changed")
			s.cancelCurrentCrystalContractTimerLocked(session, prepared.runtime, "selected_character_changed")
			s.cancelCurrentDungeonCardAutoFlipLocked(session, prepared.runtime, "selected_character_changed")
			s.cancelCurrentTournamentRewardAutoSelectTimerLocked(session, prepared.runtime, "selected_character_changed")
			prepared = currentPartyBindingDungeonSnapshotLocked(session)
			cancelled, cancelledValid = prepared, true
		}
		session.dungeon.mu.Unlock()

		// 每轮都重走完整资产预约 gate；不能只在第一次尝试时检查。
		s.lockCurrentCharacterBindingMutation(session, charID)
		if session.selectedCharacterID != expected.character || session.characterGeneration != expected.generation {
			s.mu.Unlock()
			return nil, alignedcmd.PartyState{}, nil, false
		}
		if !session.dungeon.mu.TryLock() {
			s.mu.Unlock()
			session.dungeon.mu.Lock()
			session.dungeon.mu.Unlock()
			continue
		}
		if currentPartyBindingDungeonSnapshotLocked(session) != prepared {
			session.dungeon.mu.Unlock()
			s.mu.Unlock()
			continue
		}
		locks := []*sync.Mutex{&session.party.mu}
		if resident != nil && resident != session && s.gameSessions[charID] == resident {
			locks = append(locks, &resident.party.mu)
		}
		acquired := 0
		presenceHeld := false
		unlock := func() {
			if presenceHeld {
				s.onlinePlayers.mu.RUnlock()
			}
			for i := acquired - 1; i >= 0; i-- {
				locks[i].Unlock()
			}
			session.dungeon.mu.Unlock()
			s.mu.Unlock()
		}
		var blocked *sync.Mutex
		for _, lock := range locks {
			if !lock.TryLock() {
				blocked = lock
				break
			}
			acquired++
		}
		if blocked != nil {
			unlock()
			blocked.Lock()
			blocked.Unlock()
			continue
		}
		if s.onlinePlayers != nil {
			if !s.onlinePlayers.mu.TryRLock() {
				unlock()
				s.onlinePlayers.mu.RLock()
				s.onlinePlayers.mu.RUnlock()
				continue
			}
			presenceHeld = true
			var current *gameSession
			if key, ok := s.onlinePlayers.byChar[charID]; ok {
				if info := s.onlinePlayers.byArea[key][charID]; info != nil {
					current = info.Session
				}
			}
			if current != resident {
				unlock()
				continue
			}
		}
		if resident != nil && (resident.selectedCharacterID != residentIdentity.character || resident.characterGeneration != residentIdentity.generation) {
			unlock()
			continue
		}
		state := alignedcmd.PartyState{}
		if len(locks) == 2 {
			state = cloneRuntimePartyState(resident.party.state)
			state.Members = runtimePartyMembers(state)
		}
		return resident, state, unlock, true
	}
}
