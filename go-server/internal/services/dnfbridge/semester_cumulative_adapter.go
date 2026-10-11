package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	oe "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"strings"
)

// Native event216 ACK shape. Enabling the separate default-OFF route requires
// actual client permits654 AND689 review.
func buildCurrentSemesterCumulativeACK(success bool) []byte {
	if success {
		b := make([]byte, 5)
		b[0] = 1
		binary.LittleEndian.PutUint32(b[1:], 216)
		return b
	}
	// Actual216 consumer requires both opaque bytes; a6B error underflows.
	// Their semantic error names have not been established.
	b := make([]byte, 7)
	b[1] = 1
	binary.LittleEndian.PutUint32(b[2:], 216)
	b[6] = 1
	return b
}
func (s *Service) currentSemesterCumulativeCatalog(ctx context.Context) (*oe.SemesterCatalog, error) {
	s.initialEquipmentMu.Lock()
	a, e := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if e != nil {
		return nil, e
	}
	return oe.LoadSemesterCatalog(ctx, a)
}
func (s *Service) claimCurrentSemesterCumulative(session *gameSession, index uint32) (oe.SemesterSnapshot, error) {
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return oe.SemesterSnapshot{}, oe.ErrOwnerUnavailable
	}
	return s.claimCurrentSemesterCumulativeForIdentity(session, identity, index)
}

// A dispatcher must preserve this exact identity through claim and publication.
func (s *Service) claimCurrentSemesterCumulativeForIdentity(session *gameSession, identity boundGameSessionCharacter, index uint32) (oe.SemesterSnapshot, error) {
	if !s.currentEpicBenefitsEnabled() || session == nil || !session.loginPacketSeen || !session.accountFromToken || index >= 3 {
		return oe.SemesterSnapshot{}, oe.ErrOwnerUnavailable
	}
	if identity.session != session || !s.boundGameSessionCharacterCurrent(identity) {
		return oe.SemesterSnapshot{}, oe.ErrOwnerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	catalog, e := s.currentSemesterCumulativeCatalog(ctx)
	if e != nil {
		return oe.SemesterSnapshot{}, e
	}
	g, ok := s.repositoryGroup()
	if !ok || g.MailboxAssets == nil {
		return oe.SemesterSnapshot{}, oe.ErrOwnerUnavailable
	}
	items, e := s.currentPVFItemCatalog()
	if e != nil {
		return oe.SemesterSnapshot{}, e
	}
	rawAccount := strings.TrimSpace(session.accountID)
	account := s.accountIDForSession(session)
	char := strconv.Itoa(int(identity.character))
	reservation, e := s.reserveCurrentCharacterAssets(ctx, identity, rawAccount)
	if e != nil {
		return oe.SemesterSnapshot{}, e
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	// Claim cannot create/restart a global campaign. Sampling after reservation
	// avoids interpreting wait time as a clock rollback against a later commit.
	campaign, found, e := oe.LoadEpicCampaign(ctx, g.Settings)
	if e != nil {
		return oe.SemesterSnapshot{}, e
	}
	if !found {
		return oe.SemesterSnapshot{}, oe.ErrOwnerUnavailable
	}
	now := s.gameplayNow()
	validate := func() bool {
		checkedNow := s.gameplayNow() // gameplayNow takes Service.mu internally.
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.currentCharacterAssetReservationValidLocked(reservation) && session.loginPacketSeen && session.accountFromToken && ctx.Err() == nil && s.currentEpicBenefitsEnabled() && campaign.Active(checkedNow)
	}
	return oe.ClaimSemesterReward(ctx, g, catalog, oe.SemesterClaimCommand{AccountID: account, CharacterID: char, Campaign: campaign, Now: now, Index: index, Validate: validate, Deliver: func(mail repo.MailboxRepository, r oe.AttendancePVFItemReward, key string) (string, error) {
		d, e := items.ResolveItem(uint32(r.ItemID))
		if e != nil {
			return "", e
		}
		if d.Kind != dungeonDropItemStackable {
			return "", fmt.Errorf("semester reward kind")
		}
		d, e = currentPVFItemDefinitionForGrantAt(d, now)
		if e != nil {
			return "", e
		}
		return repo.AppendSystemMail(ctx, mail, repo.SystemMailDelivery{RecipientCharacterID: char, Title: "新学期累计奖励", Body: "累计完成指定地下城8、16或24天的奖励，请查收附件。", Source: key, CreatedAt: now, Attachments: []repo.MailAttachment{{ItemID: r.ItemID, Count: r.Count, ExpireAt: d.ExpirationDate}}})
	}})
}

// Separate post-commit publisher. Owner must still be canonical.
// ACK689 does not modify event53; real654 body and ordinary mail alarm are both
// necessary. A transport failure does not undo an already committed reward.
func (s *Service) publishCurrentSemesterCumulative(session *gameSession, identity boundGameSessionCharacter, state oe.SemesterSnapshot) error {
	if identity.session != session || !s.boundGameSessionCharacterCurrent(identity) {
		return oe.ErrOwnerUnavailable
	}
	b, e := oe.BuildSemesterNotification(state)
	if e != nil {
		return e
	}
	if e := s.sendGameUpperRawClass(session, 689, buildCurrentSemesterCumulativeACK(true), 1); e != nil {
		return e
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return oe.ErrOwnerUnavailable
	}
	if e = s.sendGameUpperRawClass(session, 654, b, 0); e != nil {
		return e
	}
	if len(state.MailIDs) > 0 && s.boundGameSessionCharacterCurrent(identity) {
		return s.sendMailboxAlarmForSession(session, identity.character, "semester216")
	}
	return nil
}
