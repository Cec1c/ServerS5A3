package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/platform/eventloop"
)

// Bound one owner turn, then yield and continue with no artificial delay.
// Idle helpers do not poll; a successfully written drop announcement wakes them.
const helperAutoPickupLimit = 64
const helperLeaseDuration = 15 * time.Second

type helperLootPolicy struct {
	Gold          bool  `json:"gold"`
	EquipmentMask uint8 `json:"equipmentMask"`
	StackableMask uint8 `json:"stackableMask"`
}

func (p helperLootPolicy) valid() bool {
	return p.EquipmentMask <= 127 && p.StackableMask <= 127 && (p.Gold || p.EquipmentMask != 0 || p.StackableMask != 0)
}
func (p helperLootPolicy) accepts(d *runtimeDungeonDrop) bool {
	if d == nil {
		return false
	}
	if d.isGold() {
		return p.Gold
	}
	if d.Item.Rarity < 0 || d.Item.Rarity > 6 {
		return false
	}
	mask := uint8(0)
	switch d.Item.Kind {
	case dungeonDropItemEquipment:
		mask = p.EquipmentMask
	case dungeonDropItemStackable:
		mask = p.StackableMask
	default:
		return false
	}
	return mask&(1<<uint(d.Item.Rarity)) != 0
}

type currentPlayerHelper struct {
	mu                  sync.Mutex
	character           uint16
	characterGeneration uint64
	generation          uint64
	pending             bool
	scheduleSerial      uint64
	enabled, paused     bool
	recoverable         bool
	timer               string
	queue               gameplayTimeQueue
	policy              helperLootPolicy
	revision            uint64
	owner, lease        string
	leaseUntil          time.Time
	leaseSequence       uint64
}

// Called with h.mu held. Only expiry is eligible for automatic reacquisition;
// explicit stops and character changes must remain stopped.
func expireCurrentHelperLeaseLocked(h *currentPlayerHelper, now time.Time) {
	if !h.enabled || now.Before(h.leaseUntil) {
		return
	}
	h.enabled = false
	h.recoverable = true
	h.owner = ""
	h.lease = ""
	h.revision++
	h.pending = false
	h.scheduleSerial++
	if h.queue != nil && h.timer != "" {
		h.queue.Cancel(h.timer)
	}
}

func helperAutoPickupAvailable() bool {
	if !playerHelperReferenceEnabled() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DNFBRIDGE_HELPER_AUTOPICKUP"))) {
	case "on", "true", "1":
		return true
	}
	return false
}
func (s *Service) startCurrentPlayerHelper(p *gameSession) error {
	if !helperAutoPickupAvailable() || p == nil || !p.loginPacketSeen || !p.accountFromToken || p.selectedCharacterID == 0 {
		return nil
	}
	id, ok := s.boundGameSessionCharacterSnapshot(p)
	if !ok {
		return nil
	}
	queue := s.ensureGameplayTimeQueue()
	h := &p.helper
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.character == id.character && h.characterGeneration == id.generation {
		return nil
	}
	if h.character != 0 {
		return fmt.Errorf("helper owns previous character")
	}
	h.character = id.character
	h.characterGeneration = id.generation
	h.generation++
	h.enabled = false
	h.recoverable = false
	h.paused = false
	h.queue = queue
	// Old persisted !loot on preferences never bypass a configured filter/lease.
	return nil
}
func (s *Service) stopCurrentPlayerHelper(p *gameSession) {
	if p == nil {
		return
	}
	h := &p.helper
	h.mu.Lock()
	q, name := h.queue, h.timer
	h.generation++
	h.character = 0
	h.characterGeneration = 0
	h.pending = false
	h.scheduleSerial++
	h.enabled = false
	h.recoverable = false
	h.paused = false
	h.timer = ""
	h.queue = nil
	h.owner = ""
	h.lease = ""
	h.revision++
	h.mu.Unlock()
	if q != nil && name != "" {
		q.Cancel(name)
	}
}
func (s *Service) armCurrentPlayerHelperLocked(p *gameSession) error {
	h := &p.helper
	if !h.enabled || h.paused || h.character == 0 || h.queue == nil || h.pending {
		return nil
	}
	// The immutable owner identity is captured at activation. Drop notifications
	// can be written by a peer, so do not read mutable session fields here.
	character, generation := h.character, h.characterGeneration
	serial := h.generation
	h.scheduleSerial++
	ticket := h.scheduleSerial
	h.pending = true
	h.timer = fmt.Sprintf("dnf-player-helper:%s:%d", p.connID, character)
	queue := h.queue
	var fire func(time.Time)
	fire = func(time.Time) {
		err := s.tryPostGameSessionCharacterEvent(p, "player-helper-drop-ready", character, generation, func() error {
			h.mu.Lock()
			current := h.generation == serial && h.scheduleSerial == ticket && h.pending
			if current {
				h.pending = false
			}
			h.mu.Unlock()
			if !current {
				return nil
			}
			return s.runCurrentPlayerHelper(p, serial)
		})
		if err != nil {
			h.mu.Lock()
			if h.generation == serial && h.scheduleSerial == ticket {
				// Never wait for one player's full inbox on the shared gameplay
				// scheduler. A single coalesced retry yields to other players.
				if errors.Is(err, eventloop.ErrQueueFull) && h.pending && h.enabled && queue.Now().Before(h.leaseUntil) {
					if retryErr := queue.ScheduleAfter(h.timer, 5*time.Millisecond, fire); retryErr == nil {
						h.mu.Unlock()
						return
					}
				}
				h.pending = false
			}
			h.mu.Unlock()
			if !isClosedGameSessionEventError(err) {
				s.logGameEvent(p, "player-helper-submit-failed", "error", err)
			}
		}
	}
	err := queue.ScheduleAfter(h.timer, 0, fire)
	if err != nil {
		h.pending = false
	}
	return err
}

// Called only after a drop-bearing notification has been written successfully,
// outside wireMu. It only schedules work: the monster handler may still hold
// dungeon.mu. Consecutive kills coalesce into one pending owner turn.
func (s *Service) wakeCurrentPlayerHelper(p *gameSession) {
	if p == nil || !helperAutoPickupAvailable() {
		return
	}
	h := &p.helper
	h.mu.Lock()
	err := s.armCurrentPlayerHelperLocked(p)
	h.mu.Unlock()
	if err != nil {
		s.logGameEvent(p, "player-helper-drop-wake-failed", "error", err)
	}
}
func currentHelperPickupKeys(r *runtimeDungeonState, actor uint16, policy helperLootPolicy) []uint32 {
	if r == nil || r.Session == nil || r.Room == nil || r.DropOwner == nil || actor == 0 {
		return nil
	}
	scene, ok := r.Session.Scene()
	if !ok {
		return nil
	}
	room := runtimeDungeonRoomKeyFromScene(scene)
	var keys []uint32
	for k, d := range r.DropOwner.byObjectKey {
		if d != nil && d.Status == runtimeDungeonDropAvailable && d.OwnerActorObjectKey == actor && d.Room == room && !d.PartyShared && !d.MapOwnedPassiveObject && d.DiscardOrigin == nil && policy.accepts(d) {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	if len(keys) > helperAutoPickupLimit {
		keys = keys[:helperAutoPickupLimit]
	}
	return keys
}
func (s *Service) runCurrentPlayerHelper(p *gameSession, serial uint64) error {
	if p == nil || !helperAutoPickupAvailable() {
		return nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(p)
	if !bound || p.loginRevoked.Load() {
		return nil
	}
	now := s.gameplayNow()
	h := &p.helper
	h.mu.Lock()
	expireCurrentHelperLeaseLocked(h, now)
	active := h.generation == serial && h.character == identity.character && h.enabled && !h.paused && now.Before(h.leaseUntil)
	policy := h.policy
	h.mu.Unlock()
	if !active {
		return nil
	}
	p.dungeon.mu.Lock()
	runtime := p.dungeon.runtime
	instance := runtime.lockPartyDungeonInstance()
	var keys []uint32
	if dungeonRuntimeOwnsCharacter(runtime, identity.character) && !runtime.deathReturnWaiting && (instance == nil || !instance.deadMembers[identity.character]) {
		keys = currentHelperPickupKeys(runtime, currentSceneActorObjectKey(identity.character), policy)
	}
	unlockPartyDungeonInstance(instance)
	p.dungeon.mu.Unlock()
	for _, k := range keys {
		if !s.boundGameSessionCharacterCurrent(identity) {
			return nil
		}
		now = s.gameplayNow()
		h.mu.Lock()
		expireCurrentHelperLeaseLocked(h, now)
		active = h.enabled && h.generation == serial && now.Before(h.leaseUntil)
		h.mu.Unlock()
		if !active {
			return nil
		}
		if err := s.pickupCurrentDungeonOwnedDrop(p, dungeoncmd.GetItemRequest{DropObjectKey: k}); err != nil {
			return err
		}
		p.dungeon.mu.Lock()
		current := p.dungeon.runtime
		instance := current.lockPartyDungeonInstance()
		consumed := false
		if current == runtime && current != nil && current.DropOwner != nil {
			d := current.DropOwner.byObjectKey[k]
			consumed = d != nil && d.Status == runtimeDungeonDropConsumed
		}
		unlockPartyDungeonInstance(instance)
		p.dungeon.mu.Unlock()
		if !consumed {
			h.mu.Lock()
			if h.generation == serial {
				h.paused = true
			}
			h.mu.Unlock()
			return s.sendCurrentHelperText(p, "物品暂不可拾取，已暂停。整理背包后在内辅点击应用并开启。")
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.generation != serial {
		return nil
	}
	if len(keys) == helperAutoPickupLimit {
		return s.armCurrentPlayerHelperLocked(p)
	}
	return nil
}
func (s *Service) handleCurrentHelperChat(ctx context.Context, p *gameSession, opcode uint16, message []byte) (bool, error) {
	if !playerHelperReferenceEnabled() {
		return false, nil
	}
	text := strings.TrimSpace(string(message))
	if text != "!loot" && !strings.HasPrefix(text, "!loot ") {
		return false, nil
	}
	if p == nil {
		return true, nil
	}
	if !p.accountFromToken || !p.loginPacketSeen {
		return true, s.sendGameUpperFailure(p, opcode, 1)
	}
	if _, ok := s.boundGameSessionCharacterSnapshot(p); !ok {
		return true, s.sendGameUpperFailure(p, opcode, 1)
	}
	if err := s.sendGameUpperSuccess(p, opcode, nil); err != nil {
		return true, err
	}
	if text == "!loot off" {
		h := &p.helper
		h.mu.Lock()
		h.enabled = false
		h.recoverable = false
		h.pending = false
		h.scheduleSerial++
		h.owner = ""
		h.lease = ""
		h.revision++
		if h.queue != nil && h.timer != "" {
			h.queue.Cancel(h.timer)
		}
		h.mu.Unlock()
		return true, s.sendCurrentHelperText(p, "自动拾取已关闭。")
	}
	return true, s.sendCurrentHelperText(p, "请按Ctrl+Z，在自动拾取中勾选品级并开启；!loot off可随时停止。")
}
func (s *Service) sendCurrentHelperText(p *gameSession, message string) error {
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(message))
	if err != nil || len(encoded) > 240 {
		return fmt.Errorf("helper text exceeds native bound")
	}
	var w packetWriter
	w.writeByte(0)
	w.writeUint16(0xffff)
	w.writeByte(0)
	w.writeRawDstr(encoded)
	w.writeRawDstr(nil)
	return s.sendGameUpperRawClass(p, currentAreaChatNotifyOpcode, w.bytes(), 0)
}
