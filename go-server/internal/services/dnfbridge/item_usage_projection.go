package dnfbridge

import (
	"context"
	"encoding/binary"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemusage"
	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func hasCurrentItemUsageHistory(character repo.CharacterRecord) bool {
	if hasCurrentFatiguePotionUsageHistory(character) {
		return true
	}
	for key := range character.Stats {
		if strings.HasPrefix(key, itemusage.ItemKeyPrefix) {
			return true
		}
	}
	return false
}

// Every 541 projects both maps. A zero row count clears its entire native map,
// so a fatigue-only notification must not erase persistent weekly box counts.
func buildCurrentItemUsageBodies(character repo.CharacterRecord, inventory repo.InventoryRecord, now time.Time,
	catalog fatiguePotionUsageCatalog, source fatiguePotionUsageSource) ([][]byte, bool) {
	if catalog == nil || source == nil || !hasCurrentItemUsageHistory(character) {
		return nil, false
	}
	daily, weekly := map[uint32]byte{}, map[uint32]byte{}
	if bodies, ok := buildCurrentFatiguePotionUsageBodies(character, inventory, now, catalog, source); ok {
		for _, body := range bodies {
			for at := 1; at < 1+int(body[0])*5; at += 5 {
				daily[binary.LittleEndian.Uint32(body[at:at+4])] = body[at+4]
			}
		}
	}
	for key := range character.Stats {
		if !strings.HasPrefix(key, itemusage.ItemKeyPrefix) {
			continue
		}
		id, ok := fatiguePotionUsageCandidateID(strings.TrimPrefix(key, itemusage.ItemKeyPrefix))
		if !ok {
			continue
		}
		item, err := catalog.ResolveItem(id)
		if err != nil || item.Kind != dungeonDropItemStackable || item.PVFPath == "" {
			continue
		}
		text, err := source.ReadText(item.PVFPath)
		if err != nil {
			continue
		}
		doc, err := pvf.Parse(item.PVFPath, text)
		if err != nil {
			continue
		}
		if action, _ := doc.Text("action type"); strings.EqualFold(action, "[add fatigue]") {
			continue
		}
		definition, err := itemusage.FromPVF(id, doc)
		if err != nil || definition == nil {
			continue
		}
		d, w := itemusage.Counts(character, *definition, now)
		if definition.Daily {
			daily[id] = d
		}
		if definition.Weekly {
			weekly[id] = w
		}
	}
	if len(daily) == 0 && len(weekly) == 0 {
		return nil, false
	}
	return encodeCurrentItemUsageBodies(daily, weekly), true
}

func encodeCurrentItemUsageBodies(daily, weekly map[uint32]byte) [][]byte {
	pages := func(rows map[uint32]byte) [][]byte {
		ids := make([]uint32, 0, len(rows))
		for id := range rows {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		if len(ids) == 0 {
			return [][]byte{{0}}
		}
		var result [][]byte
		for start := 0; start < len(ids); start += 255 {
			end := start + 255
			if end > len(ids) {
				end = len(ids)
			}
			body := make([]byte, 1, 1+(end-start)*5)
			body[0] = byte(end - start)
			for _, id := range ids[start:end] {
				var row [5]byte
				binary.LittleEndian.PutUint32(row[:4], id)
				row[4] = rows[id]
				body = append(body, row[:]...)
			}
			result = append(result, body)
		}
		return result
	}
	d, w := pages(daily), pages(weekly)
	n := len(d)
	if len(w) > n {
		n = len(w)
	}
	result := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		di, wi := i, i
		if di >= len(d) {
			di = len(d) - 1
		}
		if wi >= len(w) {
			wi = len(w) - 1
		}
		// Repeat the last nonempty shorter page. Emitting zero here would
		// clear the rows already received in that map on a preceding page.
		body := append([]byte(nil), d[di]...)
		body = append(body, w[wi]...)
		result = append(result, body)
	}
	return result
}

func (s *Service) sendCurrentItemUsageForSession(session *gameSession, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	id := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	character, found, err := repos.Character.Load(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	return s.sendCurrentFatiguePotionUsageForCharacter(ctx, session, character, source)
}
