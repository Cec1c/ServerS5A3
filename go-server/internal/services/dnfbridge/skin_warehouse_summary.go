package dnfbridge

import (
	"context"
	"encoding/binary"
	"math"
	"sort"
	"time"

	"longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skinwarehouse"
	"longheng.io/server/internal/modules/dnf/weaponskin"
)

const (
	skinSummaryPageSize   = 5
	skinSummaryReplyHead  = 24
	skinSummaryReplyEntry = 16
)

type skinSummaryRequest struct {
	id, epoch uint32
	page      uint16
}

type skinSummaryEntry struct {
	skinwarehouse.Acquisition
	expiresAt uint32
}

type skinSummaryPage struct {
	page, pages, appliedFont uint16
	entries                  []skinSummaryEntry
}

func decodeSkinSummaryRequest(body []byte) (skinSummaryRequest, bool) {
	if len(body) != 20 || string(body[:4]) != "WSO1" || binary.LittleEndian.Uint32(body[16:]) != weaponskin.CatalogChecksum {
		return skinSummaryRequest{}, false
	}
	request := skinSummaryRequest{
		id: binary.LittleEndian.Uint32(body[4:8]), epoch: binary.LittleEndian.Uint32(body[8:12]),
		page: binary.LittleEndian.Uint16(body[12:14]),
	}
	return request, request.id != 0 && request.epoch != 0 && request.page != 0 && body[14] == 0 && body[15] == 0
}

func skinSummaryForCharacter(character dnfrepo.CharacterRecord, page uint16, now time.Time, available func(weaponskin.Appearance) bool) skinSummaryPage {
	active, fonts := inventory.DamageFontStateFromStats(character.Stats, now)
	ownedFonts := make(map[uint32]uint32, len(fonts))
	for _, font := range fonts {
		ownedFonts[uint32(font.FontIndex)] = font.ExpiresAt
	}
	var entries []skinSummaryEntry
	for _, acquired := range skinwarehouse.Acquisitions(character.Stats) {
		switch acquired.Kind {
		case skinwarehouse.Weapon:
			look, found := weaponskin.FromAppearance(acquired.ID)
			if found && look.Allows(character.Job) && weaponskin.Registered(character, look) && available(look) {
				entries = append(entries, skinSummaryEntry{Acquisition: acquired})
			}
		case skinwarehouse.DamageFont:
			if expires, owned := ownedFonts[acquired.ID]; owned {
				entries = append(entries, skinSummaryEntry{Acquisition: acquired, expiresAt: expires})
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		left, right := entries[i], entries[j]
		if left.Millis != right.Millis {
			return left.Millis > right.Millis
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.ID < right.ID
	})
	pages := uint16(min(math.MaxUint16, max(1, (len(entries)+skinSummaryPageSize-1)/skinSummaryPageSize)))
	page = min(pages, max(1, page))
	first := (int(page) - 1) * skinSummaryPageSize
	return skinSummaryPage{page: page, pages: pages, appliedFont: active, entries: entries[first:min(first+skinSummaryPageSize, len(entries))]}
}

func (s *Service) handleSkinWarehouseSummary(session *gameSession, body []byte) error {
	request, ok := decodeSkinSummaryRequest(body)
	if !ok || s.options.currentClientVersion != "official-24.9" {
		return nil
	}
	identity, bound, ok := s.playerStatsGameIdentity(session)
	if !ok {
		return nil
	}
	current := func() bool {
		return s.boundGameSessionCharacterCurrent(bound) && !session.loginRevoked.Load() &&
			session.townSceneReadyCharacterID == bound.character && !currentDungeonSceneActive(session)
	}
	if !current() {
		return s.sendSkinSummary(session, request, 1, skinSummaryPage{})
	}
	now := s.gameplayNow()
	if now.Sub(session.skinWarehouseSummaryLastRequest) < 150*time.Millisecond {
		return s.sendSkinSummary(session, request, 2, skinSummaryPage{})
	}
	session.skinWarehouseSummaryLastRequest = now
	ctx, cancel := context.WithTimeout(session.ctx, createWriteTimeout)
	defer cancel()
	repos, ok := s.repositoryGroupFor(ctx)
	if !ok || repos.Character == nil {
		return s.sendSkinSummary(session, request, 1, skinSummaryPage{})
	}
	character, found, err := repos.Character.Load(ctx, identity.CharacterID)
	if err != nil || !found || character.CharacterID != identity.CharacterID || character.AccountID != identity.AccountID {
		return s.sendSkinSummary(session, request, 1, skinSummaryPage{})
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return s.sendSkinSummary(session, request, 1, skinSummaryPage{})
	}
	summary := skinSummaryForCharacter(character, request.page, now, func(a weaponskin.Appearance) bool {
		return weaponSkinAssetReady(catalog, a)
	})
	if !current() {
		return nil
	}
	return s.sendSkinSummary(session, request, 0, summary)
}

func skinSummaryDate(at time.Time) uint32 {
	year, month, day := at.Local().Date()
	if year < 1 || year > 9999 {
		return 0
	}
	return uint32(year*10000 + int(month)*100 + day)
}

func encodeSkinSummary(request skinSummaryRequest, status byte, page skinSummaryPage) [skinSummaryReplyHead + skinSummaryPageSize*skinSummaryReplyEntry]byte {
	var body [skinSummaryReplyHead + skinSummaryPageSize*skinSummaryReplyEntry]byte
	copy(body[:4], "WSO2")
	binary.LittleEndian.PutUint32(body[4:8], request.id)
	binary.LittleEndian.PutUint32(body[8:12], request.epoch)
	body[12], body[13] = status, byte(len(page.entries))
	binary.LittleEndian.PutUint16(body[14:16], page.page)
	binary.LittleEndian.PutUint16(body[16:18], page.pages)
	binary.LittleEndian.PutUint16(body[18:20], page.appliedFont)
	binary.LittleEndian.PutUint32(body[20:24], weaponskin.CatalogChecksum)
	for i, entry := range page.entries {
		row := body[skinSummaryReplyHead+i*skinSummaryReplyEntry:]
		row[0] = byte(entry.Kind)
		binary.LittleEndian.PutUint32(row[4:8], entry.ID)
		binary.LittleEndian.PutUint32(row[8:12], skinSummaryDate(time.UnixMilli(entry.Millis)))
		if entry.expiresAt != 0 {
			binary.LittleEndian.PutUint32(row[12:16], skinSummaryDate(time.Unix(int64(entry.expiresAt), 0)))
		}
	}
	return body
}

func (s *Service) sendSkinSummary(session *gameSession, request skinSummaryRequest, status byte, page skinSummaryPage) error {
	body := encodeSkinSummary(request, status, page)
	return s.sendGameUpperFixed16Transport(session, weaponSkinDemoOpcode, body[:], 1, 0, false, "皮肤仓库最近获得")
}
