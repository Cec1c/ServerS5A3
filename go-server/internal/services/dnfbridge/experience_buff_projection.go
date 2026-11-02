package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"time"

	"longheng.io/server/internal/modules/dnf/experiencebuff"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Current client class0/174 reads u8 count + (u32 itemID, i32 remaining
// seconds). The item effect method multiplies seconds by 1000 and records a
// local deadline; 0 expires that item and -1 would restart its PVF duration.
// This is the native item-effect path, separate from VariousBuff and swap gear.
const currentExperiencePotionEffectMsgID uint16 = 174

type currentExperiencePotionProjection struct {
	character  uint16
	generation uint64
	items      map[uint32]bool
	revision   uint64
	timerName  string
}

func currentExperiencePotionRows(c repo.CharacterRecord, now time.Time) (map[uint32]uint32, time.Time) {
	rows := make(map[uint32]uint32)
	var next time.Time
	for _, kind := range []string{experiencebuff.Clear, experiencebuff.Quest} {
		p := "experience_potion_" + kind + "_"
		id, end := c.Stats[p+"item_id"], c.Stats[p+"until_ms"]
		// Bonus validates the same rate/time ledger used by settlement. A base
		// of 10000 preserves every allowed positive basis-point value.
		if id <= 0 || id > math.MaxUint32 || experiencebuff.Bonus(c, kind, 10000, now) == 0 {
			continue
		}
		left := end - now.UnixMilli()
		if left <= 0 || left > int64(30*24*time.Hour/time.Millisecond) {
			continue
		}
		// The client multiplies using signed i32 milliseconds. Long valid
		// ledgers are refreshed before that native interval reaches its limit.
		seconds := min((left+999)/1000, int64(math.MaxInt32/1000))
		rows[uint32(id)] = uint32(seconds)
		due := time.UnixMilli(end)
		if refresh := now.Add(time.Duration(seconds) * time.Second); refresh.Before(due) {
			due = refresh
		}
		if next.IsZero() || due.Before(next) {
			next = due
		}
	}
	return rows, next
}

func buildCurrentExperiencePotionEffects(rows map[uint32]uint32, previous map[uint32]bool) []byte {
	keys := make([]uint32, 0, len(rows)+len(previous))
	for id := range rows {
		keys = append(keys, id)
	}
	for id := range previous {
		if _, ok := rows[id]; !ok {
			keys = append(keys, id)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	body := make([]byte, 1+len(keys)*8)
	body[0] = byte(len(keys))
	for i, id := range keys {
		binary.LittleEndian.PutUint32(body[1+i*8:], id)
		binary.LittleEndian.PutUint32(body[5+i*8:], rows[id])
	}
	return body
}

// Called only from the session event owner after consumption or scene setup.
func (s *Service) sendCurrentExperiencePotionEffects(p *gameSession) error {
	identity, bound := s.boundGameSessionCharacterSnapshot(p)
	if !bound || identity.generation == 0 || p.channelExitAcknowledged {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	c, found, err := s.loadExperiencePotionCharacter(ctx, p)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return nil
	}
	now := s.gameplayNow()
	rows, next := currentExperiencePotionRows(c, now)
	old := p.experiencePotionEffects
	previous := old
	if previous.character != identity.character || previous.generation != identity.generation {
		previous = currentExperiencePotionProjection{}
	}
	if len(rows) > 0 || len(previous.items) > 0 {
		if err := s.sendGameUpperRawClass(p, currentExperiencePotionEffectMsgID, buildCurrentExperiencePotionEffects(rows, previous.items), 0); err != nil {
			return err
		}
	}
	projection := currentExperiencePotionProjection{character: identity.character, generation: identity.generation, items: make(map[uint32]bool, len(rows)), revision: old.revision + 1}
	for id := range rows {
		projection.items[id] = true
	}
	queue := s.ensureGameplayTimeQueue()
	if old.timerName != "" {
		queue.Cancel(old.timerName)
	}
	if next.IsZero() {
		p.experiencePotionEffects = projection
		return nil
	}
	// A late retry for an old projection must not replace a renewed timer,
	// even when the character and connection generation are unchanged.
	name := fmt.Sprintf("experience-potion:%p:%d:%d:%d", p, identity.character, identity.generation, projection.revision)
	projection.timerName = name
	p.experiencePotionEffects = projection
	return queue.ScheduleAfter(name, next.Sub(now), func(time.Time) { s.enqueueCurrentExperiencePotionExpiry(identity, name, 0) })
}

func (s *Service) enqueueCurrentExperiencePotionExpiry(identity boundGameSessionCharacter, name string, retry int) {
	if !s.boundGameSessionCharacterCurrent(identity) {
		return
	}
	err := s.tryPostGameSessionCharacterEvent(identity.session, "experience-potion-expiry", identity.character, identity.generation, func() error {
		if !s.boundGameSessionCharacterCurrent(identity) || identity.session.experiencePotionEffects.timerName != name {
			return nil
		}
		return s.sendCurrentExperiencePotionEffects(identity.session)
	})
	// The native timer still expires locally when a saturated queue refuses
	// refresh. Retry boundedly, without blocking the shared timer dispatcher.
	if err != nil && retry < 3 && s.boundGameSessionCharacterCurrent(identity) {
		_ = s.ensureGameplayTimeQueue().ScheduleAfter(name, time.Second, func(time.Time) { s.enqueueCurrentExperiencePotionExpiry(identity, name, retry+1) })
	}
}
