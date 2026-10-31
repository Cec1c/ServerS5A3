package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	"longheng.io/server/internal/modules/dnf/questmaterial"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type questMaterialRecipient struct {
	id      uint16
	account string
	session *gameSession
}

// Capture recipients outside the shared room lock; never lock a peer dungeon.
// The scene registration and actor binding exclude remote rooms and late joins.
func (s *Service) questMaterialRecipients(p *gameSession, rt *runtimeDungeonState, key uint32) []questMaterialRecipient {
	out := []questMaterialRecipient{{p.selectedCharacterID, s.accountIDForSession(p), p}}
	scene, ok := rt.Session.Scene()
	if !ok {
		return nil
	}
	for _, peer := range s.partyDungeonInstancePeers(rt, p.selectedCharacterID) {
		identity, bound := s.boundGameSessionCharacterSnapshot(peer.Session)
		if !bound || identity.character != peer.CharacterID || peer.Scene == nil {
			continue
		}
		other, ok := peer.Scene.Scene()
		if !ok || other.Coordinate != scene.Coordinate || other.Map.Map.ID != scene.Map.Map.ID {
			continue
		}
		if _, bound := other.RuntimeObjects[key]; !bound {
			continue
		}
		out = append(out, questMaterialRecipient{peer.CharacterID, s.accountIDForSession(peer.Session), peer.Session})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func (s *Service) prepareQuestMaterialDeath(p *gameSession, rt *runtimeDungeonState, key uint32) (func() error, error) {
	monster, ok := rt.Room.AnnouncedMonster(key)
	if !ok {
		return nil, nil
	} // APC/passive/forced cleanup keep their own protocols.
	repos, ok := s.repositoryGroup()
	if !ok {
		// Combat-only/tutorial compositions have no quest subsystem. This is not
		// a SQL failure: configured repositories still fail closed in preparation.
		return nil, nil
	}
	room, err := rt.Room.questMaterialIdentity()
	if err != nil {
		return nil, err
	}
	recipients := s.questMaterialRecipients(p, rt, key)
	rt.Room.mu.Lock()
	if rt.Room.materialRecipients == nil {
		rt.Room.materialRecipients = map[uint32][]questMaterialRecipient{}
	}
	if previous, found := rt.Room.materialRecipients[key]; found {
		recipients = previous
	} else {
		rt.Room.materialRecipients[key] = recipients
	}
	rt.Room.mu.Unlock()

	ids := make([]string, 0, len(recipients))
	accounts := map[string]string{}
	for _, v := range recipients {
		id := strconv.Itoa(int(v.id))
		ids = append(ids, id)
		accounts[id] = v.account
	}
	event := questmaterial.EventKey(key, monster.Spawn.MonsterID)
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		return questmaterial.Prepare(ctx, repos, room, event, ids, func(ctx context.Context, id string, q r.QuestRecord) ([]questmaterial.Attempt, error) {
			active := currentQuestDropActiveIDs(q)
			sort.Slice(active, func(i, j int) bool { return active[i] < active[j] })
			if len(active) == 0 {
				return nil, nil
			}
			catalog, err := s.loadQuestCatalog(ctx)
			if err != nil {
				return nil, err
			}
			if catalog == nil {
				return nil, errors.New("quest material catalog unavailable")
			}
			candidates := currentQuestDropMatchEnemy(catalog, active, int64(rt.Dungeon.ID), int64(rt.Request.Difficulty), 1, monster.Spawn.MonsterID)
			helper := false
			growth := int64(0)
			if len(candidates) > 0 && repos.Account != nil {
				a, found, e := repos.Account.Load(ctx, accounts[id])
				if e != nil {
					return nil, e
				}
				helper = found && premium.Active(a, premium.DevilSlotType(premium.DevilSlotQuestHelper), time.Now().UTC())
			}
			if effect, active := s.currentGrowthContractEffect(ctx, accounts[id]); active {
				growth = effect.QuestItemDropRatePercent
			}
			seed := sha256.Sum256([]byte(room + "/" + event + "/" + id))
			rng := newCurrentDungeonDropLCG(binary.LittleEndian.Uint32(seed[:4]))
			out := make([]questmaterial.Attempt, 0, len(candidates))
			for _, c := range candidates {
				// Preserve the existing per-attempt PVF roll; personal cap is
				// applied atomically against this character's current inventory.
				n := currentQuestDropRoll(int(rng.NextUint32()), c.Count, currentQuestItemDropRate(c.DropRate, growth, helper), 0, 0)
				out = append(out, questmaterial.Attempt{QuestID: c.QuestID, ItemID: c.ItemID, Count: n, MaxStack: c.MaxStack})
			}
			return out, nil
		})
	}, nil
}

func (rm *runtimeDungeonRoom) questMaterialIdentity() (string, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.materialID == "" {
		id, err := questmaterial.NewID()
		if err != nil {
			return "", err
		}
		rm.materialID = id
	}
	return rm.materialID, nil
}

func (s *Service) recoverQuestMaterials(p *gameSession) error {
	if p == nil || p.selectedCharacterID == 0 {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return errDungeonQuestRepositoryUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := questmaterial.Recover(ctx, repos, s.accountIDForSession(p), strconv.Itoa(int(p.selectedCharacterID)), s.grantQuestMaterialInventory)
	if err != nil {
		return err
	}
	seen := map[int16]bool{}
	slots := []int16{}
	for _, slot := range result.Slots {
		if !seen[slot] {
			seen[slot] = true
			slots = append(slots, slot)
		}
	}
	if len(slots) > 0 {
		if err := s.sendCurrentDungeonQuestDropRefresh(p, result.Inventory, slots, 0); err != nil {
			return err
		}
		return questmaterial.Acknowledge(ctx, repos, strconv.Itoa(int(p.selectedCharacterID)), result.Notices)
	}
	return nil
}

func (s *Service) dispatchQuestMaterialPeer(peer *gameSession) {
	identity, bound := s.boundGameSessionCharacterSnapshot(peer)
	if !bound {
		return
	}
	if err := s.tryPostGameSessionCharacterEvent(peer, "quest-material-delivery", identity.character, identity.generation, func() error { return s.recoverQuestMaterials(peer) }); err != nil {
		s.logGameEvent(peer, "game-quest-material-delivery-deferred", "error", err)
	}
}

func (s *Service) armQuestMaterialRecovery(p *gameSession) error {
	if p == nil || p.selectedCharacterID == 0 {
		return nil
	}
	id, generation, err := gameSessionCharacterEventIdentity(p, p.selectedCharacterID)
	if err != nil {
		return err
	}
	queue := s.ensureGameplayTimeQueue()
	if queue == nil {
		return errGameplayTimeQueueUnavailable
	}
	return queue.ScheduleAfter(fmt.Sprintf("quest-material:%s", p.connID), 5*time.Second, func(time.Time) {
		_ = s.postGameSessionCharacterEvent(p, "quest-material-recovery", id, generation, func() error {
			if err := s.recoverQuestMaterials(p); err != nil {
				s.logGameEvent(p, "game-quest-material-recovery-deferred", "error", err)
			}
			return s.armQuestMaterialRecovery(p)
		})
	})
}
func (s *Service) cancelQuestMaterialRecovery(p *gameSession) {
	if s == nil || p == nil {
		return
	}
	s.mu.Lock()
	queue := s.gameplayTimers
	s.mu.Unlock()
	if queue != nil {
		queue.Cancel(fmt.Sprintf("quest-material:%s", p.connID))
	}
}

func (s *Service) commitQuestMaterialDeath(p *gameSession, rt *runtimeDungeonState, key uint32) (runtimeDungeonDeathTarget, bool, error) {
	prepare, err := s.prepareQuestMaterialDeath(p, rt, key)
	if err != nil {
		return runtimeDungeonDeathTarget{}, false, err
	}
	return rt.Room.commitActorDeathReportPrepared(key, rt.Session, true, prepare)
}

func (s *Service) grantQuestMaterialInventory(inv *r.InventoryRecord, a questmaterial.Attempt) ([]int16, error) {
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return nil, err
	}
	def, err := catalog.ResolveItem(a.ItemID)
	if err != nil {
		return nil, err
	}
	remaining := a.Count
	slots := []int16{}
	for remaining > 0 {
		before := currentQuestDropHeldCount(*inv, int64(a.ItemID))
		slot, err := currentQuestDropGrantStack(inv, def, uint32(remaining))
		if errors.Is(err, errDungeonPickupInventoryFull) {
			return nil, questmaterial.ErrBagFull
		}
		if err != nil {
			return nil, err
		}
		added := currentQuestDropHeldCount(*inv, int64(a.ItemID)) - before
		if added <= 0 || added > remaining {
			return nil, errors.New("invalid quest material inventory delta")
		}
		key := currentCeraShopInventorySlotKey(r.MainInventoryListType, slot)
		stack := inv.Slots[key]
		entry := currentItemListEntryFromStack(r.MainInventoryListType, slot, stack)
		stack.RawEntry = append([]byte(nil), entry.data[:]...)
		inv.Slots[key] = stack
		slots = append(slots, slot)
		remaining -= added
	}
	return slots, nil
}
