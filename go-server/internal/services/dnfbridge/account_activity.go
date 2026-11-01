package dnfbridge

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 复用每轮128位随机nonce；账号、角色、行为与房间/掉落对象共同构成幂等编号。
func (s *Service) currentActivityEvent(session *gameSession, r *runtimeDungeonState, metric, source, suffix string) (*repo.ActivityEvent, error) {
	instance := r.lockPartyDungeonInstance()
	defer unlockPartyDungeonInstance(instance)
	return s.currentActivityEventPartyLocked(session, r, metric, source, suffix)
}

// Caller owns r.partyInstance.mu, when present. Pickup keeps that lock through
// ownership validation, the item/activity transaction and consuming the drop;
// building its activity event must not acquire the same mutex again.
func (s *Service) currentActivityEventPartyLocked(session *gameSession, r *runtimeDungeonState, metric, source, suffix string) (*repo.ActivityEvent, error) {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Activity == nil {
		return nil, nil
	}
	if session == nil || r == nil || r.Dungeon.ID <= 0 {
		return nil, repo.ErrActivityInvalid
	}
	if err := ensureCurrentDungeonCycleClearNonce(r); err != nil {
		return nil, err
	}
	nonce := fmt.Sprintf("%016x%016x", uint64(r.dungeonCycleClearNonceHi), uint64(r.dungeonCycleClearNonceLo))
	if instance := r.partyInstance; instance != nil {
		if instance.activityNonce == "" {
			instance.activityNonce = nonce
		}
		nonce = instance.activityNonce
	}
	id := fmt.Sprintf("%s:%d:%s:%s", nonce, session.selectedCharacterID, metric, suffix)
	return &repo.ActivityEvent{ID: id, AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(session.selectedCharacterID)), Metric: metric, Amount: 1, OccurredAt: s.gameplayNow(), DungeonID: r.Dungeon.ID, Source: source}, nil
}

// 常规深渊只在最终冻结波次全部退役时完成；不借用普通地下城通关计数。
// 开始包op235是可选通知；实际波次仍在册且全部击败才记账，不以该通知作前置。
func currentNormalAbyssEpicClearReady(r *runtimeDungeonState, group int64) bool {
	if r == nil || r.Room == nil || group <= 0 {
		return false
	}
	found := false
	for _, actor := range r.Room.Snapshot().ExtendedActors {
		if actor.Kind != runtimeDungeonActorSpecialMonster || actor.HellPartyGroupID != group {
			continue
		}
		found = true
		if actor.State != runtimeDungeonMonsterDefeated {
			return false
		}
	}
	return found
}

func currentActivityHellReady(r *runtimeDungeonState, scene worldmap.DungeonRoomScene) bool {
	if r == nil || r.Room == nil || !currentHellPartySceneIsHellRoom(r, scene) {
		return false
	}
	wave, _, ok := abyssEpicFragmentFinalWaveFor(r, abyssEpicFragmentFinalWaveRoom)
	if !ok || len(wave.GroupIDs) == 0 {
		return false
	}
	state := currentDungeonHellPartyClearState(r, scene)
	if state.WaveTotal == 0 || state.WaveRemaining != 0 {
		return false
	}
	for _, group := range wave.GroupIDs {
		if !currentNormalAbyssEpicClearReady(r, group) {
			return false
		}
	}
	return true
}

func (s *Service) recordCurrentActivityAbyss(session *gameSession, r *runtimeDungeonState, scene worldmap.DungeonRoomScene, rift bool) error {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Activity == nil || r == nil || r.activityAbyssRecorded {
		return nil
	}
	event, recipients := r.activityAbyssEvent, r.activityAbyssRecipients
	if event == nil {
		if !rift && !currentActivityHellReady(r, scene) {
			return nil
		}
		if rift && ((r.riftLoot == nil && r.epicEntry == nil) || !r.bossDieCheckAccepted || r.Session == nil || r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted) {
			return nil
		}
		source := "hell_party"
		if rift {
			source = "rift"
			if r.epicEntry != nil {
				source = "epic_road"
			}
		}
		var err error
		event, err = s.currentActivityEvent(session, r, repo.ActivityAbyss, source, "encounter")
		if err != nil || event == nil {
			return err
		}
		recipients = []dungeonDropRecipient{{CharacterID: session.selectedCharacterID, AccountID: event.AccountID}}
		if r.partyInstance != nil {
			instance := r.lockPartyDungeonInstance()
			if instance.activityAbyssAt.IsZero() {
				instance.activityAbyssAt = event.OccurredAt
				for _, id := range partyDungeonMemberCharacterIDsLocked(r, session.selectedCharacterID) {
					if id == session.selectedCharacterID {
						continue
					}
					member, memberScene := instance.sessions[id], instance.scenes[id]
					if member == nil || memberScene == nil || !currentActivitySameRoom(memberScene.Snapshot().Scene, scene) {
						continue
					}
					recipients = append(recipients, dungeonDropRecipient{CharacterID: id, AccountID: s.accountIDForSession(member)})
				}
				instance.activityAbyssRecipients = append([]dungeonDropRecipient(nil), recipients...)
			}
			event.OccurredAt = instance.activityAbyssAt
			recipients = append([]dungeonDropRecipient(nil), instance.activityAbyssRecipients...)
			unlockPartyDungeonInstance(instance)
		}
		// 冻结完成时间与在场成员；跨日重试、换房及新加入成员不改变原事件归属。
		r.activityAbyssEvent, r.activityAbyssRecipients = event, recipients
	}
	if repos.CharacterSettlement == nil {
		return repo.ErrActivityTransaction
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	for _, member := range recipients {
		e := *event
		e.AccountID = member.AccountID
		e.CharacterID = strconv.Itoa(int(member.CharacterID))
		e.ID = fmt.Sprintf("%s:%d:%s:encounter", event.ID[:32], member.CharacterID, repo.ActivityAbyss)
		if err := repos.CharacterSettlement.WithinCharacterSettlement(ctx, e.CharacterID, func(tx repo.Group) error {
			if tx.Activity == nil {
				return repo.ErrActivityTransaction
			}
			added, err := tx.Activity.Record(ctx, e)
			if err != nil {
				return err
			}
			if added && e.Source == "hell_party" {
				ch, found, err := tx.Character.Load(ctx, e.CharacterID)
				if err != nil {
					return err
				}
				if !found {
					return repo.ErrActivityTransaction
				}
				ch = repo.CloneCharacter(ch)
				repo.AddBattlePassCounter(&ch, 23, -1, 1, e.OccurredAt)
				return repo.SaveCharacterFields(ctx, tx.Character, ch, repo.CharacterFieldStats)
			}
			return nil
		}); err != nil {
			s.logGameEvent(session, "账号统计深渊完成记账失败", "char_id", member.CharacterID, "dungeon_id", r.Dungeon.ID, "source", event.Source, "event_id", e.ID, "error", err)
			return err
		}
	}
	r.activityAbyssRecorded = true
	s.logGameEvent(session, "账号统计深渊完成已记账", "char_id", session.selectedCharacterID, "dungeon_id", r.Dungeon.ID, "source", event.Source, "event_id", event.ID, "recipient_count", len(recipients))
	return nil
}

func currentActivitySameRoom(a, b worldmap.DungeonRoomScene) bool {
	return a.Coordinate == b.Coordinate && a.Map.Map.ID == b.Map.Map.ID
}

// 只统计地下城新生成装备；弃置恢复、碎片、金币走各自原分支，不进入这里。
// 调用方已持有共享副本实例锁；单人运行时无需该锁。
func (s *Service) currentActivityPickupPartyLocked(session *gameSession, r *runtimeDungeonState, drop runtimeDungeonDrop) (*repo.ActivityEvent, error) {
	if drop.DiscardOrigin != nil || drop.Item.Kind != dungeonDropItemEquipment {
		return nil, nil
	}
	metric := ""
	switch drop.Item.Rarity {
	case 3:
		metric = repo.ActivityUnique
	case 4:
		metric = repo.ActivityEpic
	case 5:
		metric = repo.ActivityLegendary
	default:
		return nil, nil
	}
	event, err := s.currentActivityEventPartyLocked(session, r, metric, "dungeon_pickup", fmt.Sprintf("%d:%d:%d:%d", drop.Room.X, drop.Room.Y, drop.Room.MapID, drop.ObjectKey))
	if err != nil || event == nil {
		return event, err
	}
	event.ItemID, event.Rarity = drop.Item.ItemID, drop.Item.Rarity
	return event, nil
}

// 街机每次入场使用独立随机编号；数据库提交结果不明时保留该编号重试。
func (s *Service) currentArcadeActivityEvent(accountID, characterID string, amount int64) (*repo.ActivityEvent, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	return &repo.ActivityEvent{ID: fmt.Sprintf("%x:%s:arcade", nonce, characterID), AccountID: accountID, CharacterID: characterID, Metric: repo.ActivityFatigue, Amount: amount, OccurredAt: s.gameplayNow(), Source: "arcade"}, nil
}
