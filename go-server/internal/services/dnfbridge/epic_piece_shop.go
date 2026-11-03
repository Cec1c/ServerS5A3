package dnfbridge

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/epicbook"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Native 104f9d0 writes one piece ID for CMD816. CMD817/818 are bodyless.
// NOTI744 (1feaef0) reads one byte and creates the PVF NPC locally. NOTI745
// (104fb80) reads u8 count and (u32 piece ID,u32 amount) pairs, up to five.
// The client maps each piece back to its equipment icon using 149d650.
type currentEpicShopEncounter struct {
	Appeared bool
	Token    string
	Members  map[uint16]bool
}

type currentEpicShop struct {
	Config         epicbook.ShopConfig
	Book           *currentEpicPieceCatalog
	Encounter      *currentEpicShopEncounter
	Target         uint32
	Materials      [5]uint32
	Refreshes      int
	Complete, Sent bool
}

func epicShopGameplayModule() gameplayModuleDefinition {
	module := gameplayModuleDefinition{Name: "epic-piece-shop", LegacyHandlers: map[uint16]gameplayHandler{}, UpperHandlers: map[uint16]gameplayHandler{}}
	for _, op := range []uint16{816, 817, 818} {
		handler := func(s *Service, session *gameSession, body []byte) error {
			return s.handleCurrentEpicShop(session, op, body)
		}
		module.LegacyHandlers[op] = func(s *Service, session *gameSession, r gameplayRequest) error { return handler(s, session, r.Body) }
		module.UpperHandlers[op] = defaultClassGameplayHandler("epic-shop-class-blocked", "epic_shop_command_class_mismatch", handler)
	}
	return module
}

// Caller owns this session's dungeon lock. Freeze the party's appearance roll
// once; each present member retains an independent, single-use exchange.
func (s *Service) offerCurrentEpicShopLocked(session *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene) error {
	if session == nil || runtime == nil || runtime.Session == nil || session.dungeon.runtime != runtime || runtime.townReturnPending || runtime.riftLoot != nil || runtime.epicEntry != nil || s.options.currentClientVersion != "official-24.9" || !s.epicPieceBook().enabled() || !s.epicPiecePush().updateEnabled() || !currentActivityHellReady(runtime, scene) {
		return nil
	}
	if runtime.epicShopChecked {
		return s.sendCurrentEpicShopAppearanceLocked(session, runtime)
	}
	items, err := s.dungeonMonsterCatalog()
	if err != nil {
		return nil
	}
	config, err := epicbook.LoadShopConfig(items.source)
	if err != nil {
		runtime.epicShopChecked = true
		s.logGameEvent(session, "epic-shop-unavailable", "reason", err)
		return nil
	}
	book, err := currentEpicPieceCatalogFor(items)
	if err != nil || book.count() < 6 {
		runtime.epicShopChecked = true
		return nil
	}
	instance := runtime.lockPartyDungeonInstance()
	var recipients []*gameSession
	var encounter *currentEpicShopEncounter
	if instance != nil {
		encounter = instance.epicShopEncounter
	}
	if encounter == nil {
		roll, rollErr := s.chooseDungeonIndex(100)
		if rollErr != nil {
			unlockPartyDungeonInstance(instance)
			return rollErr
		}
		var token [16]byte
		if _, err = rand.Read(token[:]); err != nil {
			unlockPartyDungeonInstance(instance)
			return err
		}
		encounter = &currentEpicShopEncounter{Appeared: roll < config.AppearancePercent, Token: hex.EncodeToString(token[:]), Members: map[uint16]bool{session.selectedCharacterID: true}}
		if instance != nil {
			for id, memberScene := range instance.scenes {
				if memberScene != nil && currentActivitySameRoom(memberScene.Snapshot().Scene, scene) {
					encounter.Members[id] = true
					if id != session.selectedCharacterID && instance.sessions[id] != nil {
						recipients = append(recipients, instance.sessions[id])
					}
				}
			}
			instance.epicShopEncounter = encounter
		}
	}
	unlockPartyDungeonInstance(instance)
	runtime.epicShopChecked = true
	if !encounter.Appeared || !encounter.Members[session.selectedCharacterID] {
		return nil
	}
	runtime.epicShop = &currentEpicShop{Config: config, Book: book, Encounter: encounter}
	selfErr := s.sendCurrentEpicShopAppearanceLocked(session, runtime)
	for _, peer := range recipients {
		identity, bound := s.boundGameSessionCharacterSnapshot(peer)
		if !bound {
			continue
		}
		err := s.tryPostGameSessionCharacterEvent(peer, "epic-shop-appearance", identity.character, identity.generation, func() error {
			peer.dungeon.mu.Lock()
			defer peer.dungeon.mu.Unlock()
			if !s.boundGameSessionCharacterCurrent(identity) {
				return nil
			}
			r := peer.dungeon.runtime
			if r == nil || r.Session == nil || r.partyInstance != instance {
				return nil
			}
			peerScene, ok := r.Session.Scene()
			if !ok || !currentActivitySameRoom(peerScene, scene) {
				return nil
			}
			return s.offerCurrentEpicShopLocked(peer, r, peerScene)
		})
		if err != nil {
			s.logGameEvent(peer, "epic-shop-peer-deferred", "reason", err)
		}
	}
	return selfErr
}

func (s *Service) sendCurrentEpicShopAppearanceLocked(session *gameSession, runtime *runtimeDungeonState) error {
	shop := runtime.epicShop
	if shop == nil || shop.Sent || shop.Complete {
		return nil
	}
	if err := s.sendGameUpperRawClass(session, 744, []byte{1}, 0); err != nil {
		return err
	}
	shop.Sent = true
	return nil
}

// NOTI744(0) clears the native singleton before the next scene can rebuild
// its local NPC. The encounter is retained server-side only for deduplication.
func (s *Service) clearCurrentEpicShopAppearanceLocked(session *gameSession, runtime *runtimeDungeonState) error {
	if runtime == nil || runtime.epicShop == nil || !runtime.epicShop.Sent {
		return nil
	}
	if err := s.sendGameUpperRawClass(session, 744, []byte{0}, 0); err != nil {
		return err
	}
	runtime.epicShop.Sent = false
	return nil
}

// Selection is frozen after CMD816. Replaying the choice cannot reroll a free
// material list; subsequent rerolls must use the paid, limited CMD817 path.
func (s *Service) chooseCurrentEpicShopMaterials(shop *currentEpicShop, target uint32) ([5]uint32, error) {
	var result [5]uint32
	if shop == nil || !shop.Book.isEpicPiece(target) {
		return result, epicbook.ErrShop
	}
	items, err := s.dungeonMonsterCatalog()
	if err != nil {
		return result, err
	}
	levels, err := currentAbyssEpicPieceLevelIndexFor(items)
	if err != nil {
		return result, err
	}
	level, known := levels.levelOf(target)
	if !known || level <= 0 {
		return result, epicbook.ErrShop
	}
	// Use the selected equipment's original minimum level, not the piece's
	// own level, the character level, or a contract-adjusted display level.
	// A Lv90 target draws random materials only from Lv85 through Lv90.
	pool := make([]uint32, 0, shop.Book.count()-1)
	for _, id := range shop.Book.pieces {
		materialLevel, resolved := levels.levelOf(id)
		if id != target && resolved && materialLevel > 0 && materialLevel >= level-5 && materialLevel <= level {
			pool = append(pool, id)
		}
	}
	if len(pool) < len(result) {
		return result, epicbook.ErrShop
	}
	for i := range result {
		index, err := s.chooseDungeonIndex(len(pool) - i)
		if err != nil {
			return result, err
		}
		index += i
		pool[i], pool[index] = pool[index], pool[i]
		result[i] = pool[i]
	}
	return result, nil
}

func buildCurrentEpicShopMaterials(shop *currentEpicShop) []byte {
	body := make([]byte, 41)
	body[0] = 5
	for i, id := range shop.Materials {
		binary.LittleEndian.PutUint32(body[1+i*8:], id)
		binary.LittleEndian.PutUint32(body[5+i*8:], shop.Config.Received)
	}
	return body
}

func (s *Service) handleCurrentEpicShop(session *gameSession, op uint16, body []byte) error {
	if session == nil || session.selectedCharacterID == 0 || s.options.currentClientVersion != "official-24.9" || (op == 816 && len(body) != 4) || (op != 816 && (op != 817 && op != 818 || len(body) != 0)) {
		return s.sendGameUpperFailure(session, op, 19)
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	runtime := session.dungeon.runtime
	if runtime == nil || runtime.Session == nil || runtime.townReturnPending || !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
		return s.sendGameUpperFailure(session, op, 19)
	}
	scene, ok := runtime.Session.Scene()
	shop := runtime.epicShop
	status := runtime.Session.Snapshot().Run.Status
	if !ok || (status != worldmap.DungeonRunActive && status != worldmap.DungeonRunCompleted) || !currentActivityHellReady(runtime, scene) || shop == nil || !shop.Sent || shop.Complete || !shop.Encounter.Members[session.selectedCharacterID] {
		return s.sendGameUpperFailure(session, op, 19)
	}
	if op == 816 {
		target := binary.LittleEndian.Uint32(body)
		if !shop.Book.isEpicPiece(target) || (shop.Target != 0 && shop.Target != target) {
			return s.sendGameUpperFailure(session, op, 19)
		}
		if shop.Target == 0 {
			materials, err := s.chooseCurrentEpicShopMaterials(shop, target)
			if err != nil {
				return s.sendGameUpperFailure(session, op, 19)
			}
			shop.Target, shop.Materials = target, materials
		}
		if err := s.sendGameUpperSuccess(session, op, nil); err != nil {
			return err
		}
		return s.sendGameUpperRawClass(session, 745, buildCurrentEpicShopMaterials(shop), 0)
	}
	if shop.Target == 0 || (op == 817 && shop.Refreshes >= shop.Config.RefreshLimit) {
		return s.sendGameUpperFailure(session, op, 19)
	}
	materials := shop.Materials
	if op == 817 {
		var err error
		materials, err = s.chooseCurrentEpicShopMaterials(shop, shop.Target)
		if err != nil {
			return s.sendGameUpperFailure(session, op, 19)
		}
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return s.sendGameUpperFailure(session, op, 19)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := epicbook.ApplyShop(ctx, repos, epicbook.ShopCommand{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(session.selectedCharacterID)), Encounter: shop.Encounter.Token, Config: shop.Config, Target: shop.Target, Materials: shop.Materials, Refresh: op == 817, Now: s.gameplayNow()})
	if err != nil {
		code := byte(19)
		if errors.Is(err, epicbook.ErrMaterial) {
			code = 22
		}
		if errors.Is(err, epicbook.ErrShopGold) {
			code = 21
		}
		return s.sendGameUpperFailure(session, op, code)
	}
	// Commit state before the first write. A failed response cannot repeat the
	// exchange, and a paid refresh retains its new quote for later reads.
	if op == 817 {
		shop.Materials = materials
		shop.Refreshes++
	} else {
		shop.Complete = true
	}
	if op == 817 {
		if err = s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), buildCurrentGoldStateBody(result.Gold), 0); err != nil {
			return fmt.Errorf("epic shop committed: %w", err)
		}
	} else {
		balances, buildErr := buildCurrentEpicPieceBalanceSyncBody(result.Balances, false)
		if buildErr != nil {
			return buildErr
		}
		if err = s.sendGameUpperRawClass(session, currentEpicPieceBalanceSyncOpcode, balances, 0); err != nil {
			return fmt.Errorf("epic shop committed: %w", err)
		}
	}
	if err = s.sendGameUpperSuccess(session, op, nil); err != nil {
		return err
	}
	if op == 817 {
		return s.sendGameUpperRawClass(session, 745, buildCurrentEpicShopMaterials(shop), 0)
	}
	return nil
}
