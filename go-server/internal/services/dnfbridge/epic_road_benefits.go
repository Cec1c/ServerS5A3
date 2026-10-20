package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"strings"
)

func (s *Service) currentEpicBenefitsEnabled() bool {
	return s != nil && s.options.currentClientVersion == "official-24.9" && (s.activityControls == nil || s.activityControls.Enabled(350))
}
func (s *Service) currentEpicCatalog(ctx context.Context) (*onlineevent.EpicCatalog, error) {
	s.initialEquipmentMu.Lock()
	a, e := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if e != nil {
		return nil, e
	}
	if e = validateCurrentEpicRoadActivity(a); e != nil {
		return nil, e
	}
	return onlineevent.LoadEpicCatalog(ctx, a)
}
func (s *Service) currentEpicCampaign(session *gameSession) (onlineevent.EpicCampaign, error) {
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok || !session.loginPacketSeen || !session.accountFromToken || strings.TrimSpace(session.accountID) == "" || !s.currentEpicBenefitsEnabled() {
		return onlineevent.EpicCampaign{}, onlineevent.ErrOwnerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	if _, e := s.currentEpicCatalog(ctx); e != nil {
		return onlineevent.EpicCampaign{}, e
	}
	g, ok := s.repositoryGroup()
	if !ok {
		return onlineevent.EpicCampaign{}, onlineevent.ErrOwnerUnavailable
	}
	return onlineevent.ActivateEpicCampaign(ctx, g, s.accountIDForSession(session), strconv.Itoa(int(identity.character)), s.gameplayNow(), func() bool { return s.boundGameSessionCharacterCurrent(identity) && s.currentEpicBenefitsEnabled() })
}
func buildCurrentEpicStateBody(state onlineevent.EpicSnapshot) []byte {
	b := make([]byte, 8)
	// Actual11DDE04 signed compare clamps ordinary positive progress to18000.
	// Project the same bounded progress before transport, including high-bit
	// u32 counters; keep the authoritative durable invitation count unchanged.
	progress := state.Invitations
	if progress > 18000 {
		progress = 18000
	}
	binary.LittleEndian.PutUint32(b, progress)
	binary.LittleEndian.PutUint32(b[4:], state.Claimed)
	return b
}
func buildCurrentEpicClaimACK(success bool) []byte {
	if !success {
		b := make([]byte, 6)
		b[1] = 1
		binary.LittleEndian.PutUint32(b[2:], 350)
		return b
	}
	b := make([]byte, 17)
	b[0] = 1
	binary.LittleEndian.PutUint32(b[1:], 350)
	return b
}
func (s *Service) sendCurrentEpicState(session *gameSession) error {
	campaign, e := s.currentEpicCampaign(session)
	if e != nil {
		return e
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return onlineevent.ErrOwnerUnavailable
	}
	g, ok := s.repositoryGroup()
	if !ok {
		return onlineevent.ErrOwnerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	ch, found, e := g.Character.Load(ctx, strconv.Itoa(int(identity.character)))
	if e != nil {
		return e
	}
	if !found || ch.CharacterID != strconv.Itoa(int(identity.character)) || ch.AccountID != s.accountIDForSession(session) {
		return onlineevent.ErrOwnerUnavailable
	}
	state, e := onlineevent.EpicCharacterSnapshot(ch, campaign, s.gameplayNow())
	if e != nil {
		return e
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return onlineevent.ErrOwnerUnavailable
	}
	return s.sendGameUpperRawClass(session, 1455, buildCurrentEpicStateBody(state), 0)
}
func (s *Service) claimCurrentEpicReward(session *gameSession, index uint32) (onlineevent.EpicSnapshot, error) {
	campaign, e := s.currentEpicCampaign(session)
	if e != nil {
		return onlineevent.EpicSnapshot{}, e
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return onlineevent.EpicSnapshot{}, onlineevent.ErrOwnerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	catalog, e := s.currentEpicCatalog(ctx)
	if e != nil {
		return onlineevent.EpicSnapshot{}, e
	}
	g, ok := s.repositoryGroup()
	if !ok {
		return onlineevent.EpicSnapshot{}, onlineevent.ErrOwnerUnavailable
	}
	items, e := s.currentPVFItemCatalog()
	if e != nil {
		return onlineevent.EpicSnapshot{}, e
	}
	rawAccountID := strings.TrimSpace(session.accountID)
	accountID := s.accountIDForSession(session)
	charID := strconv.Itoa(int(identity.character))
	now := s.gameplayNow()
	reservation, e := s.reserveCurrentCharacterAssets(ctx, identity, rawAccountID)
	if e != nil {
		return onlineevent.EpicSnapshot{}, e
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	validate := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.currentCharacterAssetReservationValidLocked(reservation) && session.selectedCharacterID == identity.character && session.characterGeneration == identity.generation && s.gameSessions[identity.character] == session && strings.TrimSpace(session.accountID) == rawAccountID && ctx.Err() == nil && s.currentEpicBenefitsEnabled()
	}
	return onlineevent.ClaimEpicReward(ctx, g, catalog, onlineevent.EpicClaimCommand{AccountID: accountID, CharacterID: charID, Campaign: campaign, Now: now, Index: index, Validate: validate, Deliver: func(m repo.MailboxRepository, r onlineevent.AttendancePVFItemReward, key string) (string, error) {
		d, e := items.ResolveItem(uint32(r.ItemID))
		if e != nil {
			return "", e
		}
		if d.Kind != dungeonDropItemStackable {
			return "", fmt.Errorf("epic reward kind")
		}
		d, e = currentPVFItemDefinitionForGrantAt(d, now)
		if e != nil {
			return "", e
		}
		return repo.AppendSystemMail(ctx, m, repo.SystemMailDelivery{RecipientCharacterID: charID, Title: "史诗之路累计奖励", Body: "邀请函累计消耗奖励，请查收附件。", Source: key, CreatedAt: now, Attachments: []repo.MailAttachment{{ItemID: r.ItemID, Count: r.Count, ExpireAt: d.ExpirationDate}}})
	}})
}
func (s *Service) handleCurrentEpicClaim(session *gameSession, op uint16, class byte, body []byte) (bool, error) {
	if s == nil || s.options.currentClientVersion != "official-24.9" || op != 688 || len(body) < 4 || binary.LittleEndian.Uint32(body) != 350 {
		return false, nil
	}
	if class != 1 {
		return true, nil
	}
	if len(body) != 8 || !s.currentEpicBenefitsEnabled() {
		return true, s.sendGameUpperRawClass(session, 688, buildCurrentEpicClaimACK(false), 1)
	}
	state, e := s.claimCurrentEpicReward(session, binary.LittleEndian.Uint32(body[4:]))
	if e != nil {
		s.logGameEvent(session, "game-epic-claim-rejected", "error", e)
		return true, s.sendGameUpperRawClass(session, 688, buildCurrentEpicClaimACK(false), 1)
	}
	if e = s.sendGameUpperRawClass(session, 688, buildCurrentEpicClaimACK(true), 1); e != nil {
		return true, e
	}
	if e = s.sendGameUpperRawClass(session, 1455, buildCurrentEpicStateBody(state), 0); e != nil {
		return true, e
	}
	if len(state.MailIDs) > 0 {
		return true, s.sendMailboxAlarmForSession(session, session.selectedCharacterID, "epic_road")
	}
	return true, nil
}

// Entry already has a canonical selected session and server-resolved physical
// character/account. Preserve that existing admission contract for counting.
func (s *Service) currentEpicCampaignForEntry(session *gameSession, runtime *runtimeDungeonState) (onlineevent.EpicCampaign, error) {
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok || !s.currentEpicBenefitsEnabled() || runtime == nil || runtime.Character.CharacterID != strconv.Itoa(int(identity.character)) || runtime.Character.AccountID != runtime.AccountID || runtime.AccountID != s.accountIDForSession(session) {
		return onlineevent.EpicCampaign{}, onlineevent.ErrOwnerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	if _, e := s.currentEpicCatalog(ctx); e != nil {
		return onlineevent.EpicCampaign{}, e
	}
	g, ok := s.repositoryGroup()
	if !ok {
		return onlineevent.EpicCampaign{}, onlineevent.ErrOwnerUnavailable
	}
	return onlineevent.ActivateEpicCampaign(ctx, g, runtime.AccountID, runtime.Character.CharacterID, s.gameplayNow(), func() bool { return s.boundGameSessionCharacterCurrent(identity) && s.currentEpicBenefitsEnabled() })
}
