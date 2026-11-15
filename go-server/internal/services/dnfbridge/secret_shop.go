package dnfbridge

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"strings"
	"sync"

	"longheng.io/server/internal/modules/dnf/secretshop"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Paired native client: NOTI279 chooses the NPC; NOTI280 reads a u32 count
// and 22-byte rows. CMD296 buys by item ID and quantity; CMD297 opens/closes.
// The switch must only be enabled with the native shelf-vector repair.
type currentSecretShopCatalogState struct {
	once    sync.Once
	catalog *secretshop.Catalog
	err     error
}
type currentSecretShopEncounter struct {
	NPCID    uint32
	Nonce    [2]int64
	Members  map[uint16]bool // value is the frozen black-diamond entitlement
	offersMu sync.Mutex
	offers   map[uint16][]currentSecretShopRow
}
type currentSecretShopRow struct {
	Candidate  secretshop.Candidate
	Definition dungeonDropItemDefinition
	Remaining  uint32
}
type currentSecretShop struct {
	Encounter  *currentSecretShopEncounter
	Rows       []currentSecretShopRow
	Scene      worldmap.DungeonRoomScene
	Sent, Open bool
}

var errCurrentSecretShop = errors.New("current secret shop is unavailable")

func (s *Service) blackDiamondSecretShopEnabled() bool {
	return s != nil && s.options.currentClientVersion == "official-24.9" && strings.EqualFold(strings.TrimSpace(os.Getenv("DNFBRIDGE_BLACK_DIAMOND_SECRET_SHOP")), "on")
}

func (s *Service) currentSecretShopCatalog() (*secretshop.Catalog, error) {
	s.secretShops.once.Do(func() {
		items, err := s.dungeonMonsterCatalog()
		if err != nil {
			s.secretShops.err = err
			return
		}
		s.secretShops.catalog, s.secretShops.err = secretshop.Load(items.source)
	})
	return s.secretShops.catalog, s.secretShops.err
}

func currentSecretShopEligible(runtime *runtimeDungeonState) bool {
	return runtime != nil && runtime.Session != nil && !runtime.townReturnPending && runtime.raidEntry == nil && runtime.guildDungeonClear == nil && runtime.guildPractice == nil && runtime.Tournament == nil && runtime.riftLoot == nil && runtime.epicEntry == nil && !isPVFTutorialDungeon(runtime) && !strings.Contains(strings.ToLower(runtime.Dungeon.Metadata.DungeonType), "tower") && runtime.Dungeon.ID > 0 && runtime.Dungeon.ID <= math.MaxUint32 && runtime.Session.Snapshot().Run.Status == worldmap.DungeonRunCompleted
}

func (s *Service) secretShopRoll(limit uint32) (uint32, error) {
	if limit == 0 || uint64(limit) > uint64(math.MaxInt32) {
		return 0, errCurrentSecretShop
	}
	v, err := s.chooseDungeonIndex(int(limit))
	if err != nil || v < 0 || v >= int(limit) {
		return 0, errCurrentSecretShop
	}
	return uint32(v), nil
}

// Caller owns only this actor's dungeon lock. Repository reads precede the
// party-instance lock; peer work is posted to its bounded actor queue.
func (s *Service) offerCurrentSecretShopLocked(session *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene) error {
	if !s.blackDiamondSecretShopEnabled() || session == nil || session.dungeon.runtime != runtime || !currentSecretShopEligible(runtime) || !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
		return nil
	}
	if runtime.secretShopChecked {
		return s.sendCurrentSecretShopLocked(session, runtime)
	}
	catalog, err := s.currentSecretShopCatalog()
	if err != nil {
		return err
	}
	instance := runtime.partyInstance
	members := map[uint16]bool{session.selectedCharacterID: false}
	if instance != nil {
		instance.mu.Lock()
		for id, memberScene := range instance.scenes {
			if memberScene != nil && currentActivitySameRoom(memberScene.Snapshot().Scene, scene) {
				members[id] = false
			}
		}
		instance.mu.Unlock()
	}
	if len(members) > 4 {
		return errCurrentSecretShop
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	count := 0
	for id := range members {
		members[id] = s.partyMemberBlackDiamondActive(ctx, id)
		if members[id] {
			count++
		}
	}
	var encounter *currentSecretShopEncounter
	var recipients []*gameSession
	if instance != nil {
		instance.mu.Lock()
		encounter = instance.secretShopEncounter
	}
	if encounter == nil {
		level := int(runtime.Dungeon.Metadata.BasisLevel.Value)
		if level <= 0 {
			level = runtime.entryCharacterLevel
		}
		npc, rollErr := secretshop.PickNPC(catalog, uint32(runtime.Dungeon.ID), level, count, s.secretShopRoll)
		if rollErr != nil {
			if instance != nil {
				instance.mu.Unlock()
			}
			return rollErr
		}
		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			if instance != nil {
				instance.mu.Unlock()
			}
			return err
		}
		encounter = &currentSecretShopEncounter{NPCID: npc, Nonce: [2]int64{int64(binary.LittleEndian.Uint64(nonce[:8])), int64(binary.LittleEndian.Uint64(nonce[8:]))}, Members: members}
		if instance != nil {
			instance.secretShopEncounter = encounter
			for id := range members {
				if id != session.selectedCharacterID && instance.sessions[id] != nil {
					recipients = append(recipients, instance.sessions[id])
				}
			}
		}
	}
	if instance != nil {
		instance.mu.Unlock()
	}
	runtime.secretShopChecked = true
	black, present := encounter.Members[session.selectedCharacterID]
	if present && encounter.NPCID != secretshop.DefaultNPCID {
		shop, buildErr := s.frozenCurrentSecretShop(catalog, encounter, runtime, scene, black, session.selectedCharacterID)
		if buildErr != nil {
			return buildErr
		}
		runtime.secretShop = shop
	}
	selfErr := s.sendCurrentSecretShopLocked(session, runtime)
	for _, peer := range recipients {
		identity, bound := s.boundGameSessionCharacterSnapshot(peer)
		if !bound {
			continue
		}
		postErr := s.tryPostGameSessionCharacterEvent(peer, "secret-shop-appearance", identity.character, identity.generation, func() error {
			peer.dungeon.mu.Lock()
			defer peer.dungeon.mu.Unlock()
			r := peer.dungeon.runtime
			if !s.boundGameSessionCharacterCurrent(identity) || r == nil || r.Session == nil || r.partyInstance != instance {
				return nil
			}
			peerScene, ok := r.Session.Scene()
			if !ok || !currentActivitySameRoom(peerScene, scene) {
				return nil
			}
			return s.offerCurrentSecretShopLocked(peer, r, peerScene)
		})
		if postErr != nil {
			s.logGameEvent(peer, "secret-shop-peer-deferred", "error", postErr)
		}
	}
	return selfErr
}

// A party encounter can survive one member's transport/runtime replacement.
// Retain that member's exact quote and row order; never reroll its stock IDs.
func (s *Service) frozenCurrentSecretShop(catalog *secretshop.Catalog, e *currentSecretShopEncounter, r *runtimeDungeonState, scene worldmap.DungeonRoomScene, black bool, owner uint16) (*currentSecretShop, error) {
	e.offersMu.Lock()
	defer e.offersMu.Unlock()
	if rows, known := e.offers[owner]; known {
		if len(rows) == 0 {
			return nil, nil
		}
		return &currentSecretShop{Encounter: e, Scene: scene, Rows: append([]currentSecretShopRow(nil), rows...)}, nil
	}
	shop, err := s.buildCurrentSecretShop(catalog, e, r, scene, black)
	if err != nil {
		return nil, err
	}
	if e.offers == nil {
		e.offers = map[uint16][]currentSecretShopRow{}
	}
	if shop == nil {
		e.offers[owner] = nil
		return nil, nil
	}
	e.offers[owner] = append([]currentSecretShopRow(nil), shop.Rows...)
	return shop, nil
}

func (s *Service) buildCurrentSecretShop(catalog *secretshop.Catalog, encounter *currentSecretShopEncounter, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene, black bool) (*currentSecretShop, error) {
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return nil, err
	}
	definitions := map[uint32]dungeonDropItemDefinition{}
	valid := func(c secretshop.Candidate) bool {
		d, e := items.ResolveItem(c.ItemID)
		if e != nil {
			return false
		}
		d, e = currentPVFItemDefinitionForGrantAt(d, s.gameplayNow())
		if e != nil {
			return false
		}
		if d.Kind != dungeonDropItemEquipment && d.Kind != dungeonDropItemStackable {
			return false
		}
		if currentNPCShopProductList(d) != 0 {
			return false
		}
		// Secret-shop costs are physical materials, never the account's cash wallet.
		if c.CostMode == 1 {
			cost, e := items.ResolveItem(c.CostItemID)
			if e != nil || cost.Kind != dungeonDropItemStackable || strings.Trim(cost.StackableType, "[] ") != "material" {
				return false
			}
		}
		definitions[c.ItemID] = d
		return true
	}
	level := int(runtime.Dungeon.Metadata.BasisLevel.Value)
	if level <= 0 {
		level = runtime.entryCharacterLevel
	}
	selected, err := secretshop.RollOffers(catalog, encounter.NPCID, uint32(runtime.Dungeon.ID), level, black, valid, s.secretShopRoll)
	if err != nil {
		return nil, err
	}
	shop := &currentSecretShop{Encounter: encounter, Scene: scene}
	for _, v := range selected {
		c := v.Candidate
		d := definitions[c.ItemID]
		if d.Kind == dungeonDropItemEquipment {
			c.Count = 1
		} else if black {
			percent := catalog.StackablePercent
			if v.Extra {
				percent = catalog.ExtraStackablePercent
			}
			n := uint64(c.Count) * uint64(percent) / 100
			if n == 0 || n > math.MaxInt32 {
				return nil, errCurrentSecretShop
			}
			c.Count = uint32(n)
		}
		shop.Rows = append(shop.Rows, currentSecretShopRow{Candidate: c, Definition: d, Remaining: c.Count})
	}
	if len(shop.Rows) == 0 {
		return nil, nil
	}
	return shop, nil
}

func buildCurrentSecretShopItems(shop *currentSecretShop) []byte {
	body := make([]byte, 4+22*len(shop.Rows))
	binary.LittleEndian.PutUint32(body, uint32(len(shop.Rows)))
	for i, row := range shop.Rows {
		p := body[4+i*22:]
		c := row.Candidate
		binary.LittleEndian.PutUint32(p, c.ItemID)
		p[4] = c.CostMode
		if c.CostMode == 0 {
			binary.LittleEndian.PutUint32(p[5:], uint32(c.Price))
		}
		binary.LittleEndian.PutUint32(p[9:], row.Remaining)
		binary.LittleEndian.PutUint32(p[13:], c.CostItemID)
		if c.CostMode == 1 {
			binary.LittleEndian.PutUint32(p[17:], uint32(c.Price))
		}
	}
	return body
}

func (s *Service) sendCurrentSecretShopLocked(session *gameSession, runtime *runtimeDungeonState) error {
	shop := runtime.secretShop
	if shop == nil || shop.Sent {
		return nil
	}
	if err := s.restoreCurrentSecretShopStock(session, shop); err != nil {
		return err
	}
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, shop.Encounter.NPCID)
	if err := s.sendGameUpperRawClass(session, 279, body, 0); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, 280, buildCurrentSecretShopItems(shop), 0); err != nil {
		return err
	}
	shop.Sent = true
	s.logGameEvent(session, "secret-shop-offered", "npc_id", shop.Encounter.NPCID, "rows", len(shop.Rows), "black_diamond", shop.Encounter.Members[session.selectedCharacterID])
	return nil
}

func (s *Service) clearCurrentSecretShopLocked(session *gameSession, runtime *runtimeDungeonState) error {
	if runtime == nil || runtime.secretShop == nil || !runtime.secretShop.Sent {
		return nil
	}
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, secretshop.DefaultNPCID)
	if err := s.sendGameUpperRawClass(session, 279, body, 0); err != nil {
		return err
	}
	runtime.secretShop.Sent = false
	runtime.secretShop.Open = false
	return nil
}

func secretShopGameplayModule() gameplayModuleDefinition {
	m := gameplayModuleDefinition{Name: "black-diamond-secret-shop", UpperHandlers: map[uint16]gameplayHandler{}}
	for _, op := range []uint16{296, 297} {
		m.UpperHandlers[op] = func(s *Service, session *gameSession, r gameplayRequest) error {
			if !requireDefaultGameplayClassification(s, session, r, "secret-shop-class-blocked", "secret_shop_class_mismatch") {
				return nil
			}
			return s.handleCurrentSecretShop(session, r)
		}
	}
	return m
}

func (s *Service) handleCurrentSecretShop(session *gameSession, r gameplayRequest) error {
	if !s.blackDiamondSecretShopEnabled() || session == nil || session.selectedCharacterID == 0 {
		return s.sendGameUpperFailure(session, r.Opcode, 19)
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	runtime := session.dungeon.runtime
	if !currentSecretShopEligible(runtime) || !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) || runtime.secretShop == nil {
		return s.sendGameUpperFailure(session, r.Opcode, 19)
	}
	shop := runtime.secretShop
	scene, ok := runtime.Session.Scene()
	_, member := shop.Encounter.Members[session.selectedCharacterID]
	if !ok || !member || !shop.Sent || !currentActivitySameRoom(scene, shop.Scene) {
		return s.sendGameUpperFailure(session, r.Opcode, 19)
	}
	if r.Opcode == 297 {
		if len(r.Body) != 1 || r.Body[0] > 1 {
			return nil
		}
		shop.Open = r.Body[0] == 1
		return nil
	}
	if r.Opcode != 296 || len(r.Body) != 8 || !shop.Open {
		return s.sendGameUpperFailure(session, r.Opcode, 19)
	}
	itemID, wireCount := binary.LittleEndian.Uint32(r.Body), binary.LittleEndian.Uint32(r.Body[4:])
	index := -1
	for i := range shop.Rows {
		if shop.Rows[i].Candidate.ItemID == itemID {
			index = i
			break
		}
	}
	if index < 0 {
		return s.sendGameUpperFailure(session, 296, 19)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, remaining, err := s.commitCurrentSecretShopBuy(ctx, session, shop, index, wireCount, r.Sequence)
	if err != nil {
		s.logGameEvent(session, "secret-shop-buy-rejected", "item_id", itemID, "error", err)
		return s.sendGameUpperFailure(session, 296, 19)
	}
	shop.Rows[index].Remaining = remaining
	if err = s.sendGameUpperRawClass(session, 296, buildCurrentSecretShopBuyAck(result, remaining), 1); err != nil {
		return err
	}
	return s.sendGameUpperRawClass(session, 14, buildCurrentItemUpdateBody(0, result.Updates), 0)
}

func buildCurrentSecretShopBuyAck(r currentNPCShopBuyResult, remaining uint32) []byte {
	b := make([]byte, 30)
	b[0] = 1
	binary.LittleEndian.PutUint32(b[1:], uint32(r.GoldAfter))
	copy(b[5:15], r.Item.data[:10])
	b[15] = r.Item.data[10]
	copy(b[16:18], r.Item.data[11:13])
	cost := r.CostItemID
	if cost == 0 {
		cost = math.MaxUint32
	}
	binary.LittleEndian.PutUint32(b[18:], cost)
	binary.LittleEndian.PutUint32(b[22:], uint32(r.CostItemNewCount))
	binary.LittleEndian.PutUint32(b[26:], remaining)
	return b
}
