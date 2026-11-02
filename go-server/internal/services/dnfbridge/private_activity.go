package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/activeness"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"strconv"
	"sync"
	"time"
)

var privateActivityCatalogs sync.Map

type privateActivityCatalogResult struct {
	catalog *activeness.Catalog
	err     error
}

func (s *Service) privateActivityCatalog() (*activeness.Catalog, error) {
	s.initialEquipmentMu.Lock()
	archive := s.initialEquipmentArchive
	s.initialEquipmentMu.Unlock()
	if archive == nil {
		return nil, nil
	}
	if v, ok := privateActivityCatalogs.Load(archive); ok {
		r := v.(privateActivityCatalogResult)
		return r.catalog, r.err
	}
	text, err := archive.ReadText(activeness.PVFPath)
	if errors.Is(err, platformpvf.ErrFileNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse(activeness.PVFPath, text)
	var c *activeness.Catalog
	if err == nil {
		c, err = activeness.ParseCatalog(doc)
	}
	privateActivityCatalogs.Store(archive, privateActivityCatalogResult{c, err})
	return c, err
}
func (s *Service) applyPrivateActivity(ch *repo.CharacterRecord, id uint16, amount uint32, now, eventAt time.Time) (bool, error) {
	c, err := s.privateActivityCatalog()
	if err != nil {
		return false, err
	}
	return activeness.Apply(ch, c, id, amount, now, eventAt)
}

// Event callers supply an authenticated owner and a frozen completion time.
// Character assets serialize this with fatigue, item consumption and claims.
func (s *Service) persistPrivateActivity(ctx context.Context, account, character string, id uint16, now, eventAt time.Time) error {
	c, err := s.privateActivityCatalog()
	if err != nil || c == nil {
		return err
	}
	g, ok := s.repositoryGroup()
	if !ok || g.CharacterAssets == nil {
		return activeness.ErrOwner
	}
	return g.CharacterAssets.WithinCharacterAssets(ctx, character, func(chars repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository) error {
		ch, found, err := chars.Load(ctx, character)
		if err != nil {
			return err
		}
		if !found || ch.AccountID != account || ch.CharacterID != character {
			return activeness.ErrOwner
		}
		ch = repo.CloneCharacter(ch)
		changed, err := activeness.Apply(&ch, c, id, 1, now, eventAt)
		if err != nil || !changed {
			return err
		}
		ch.UpdatedAt = now
		return repo.SaveCharacterFields(ctx, chars, ch, repo.CharacterFieldStats)
	})
}
func (s *Service) privateActivityDungeonTask(runtime *runtimeDungeonState) (uint16, error) {
	if runtime == nil {
		return 0, nil
	}
	if runtime.Dungeon.ID == 316 {
		return 2, nil
	}
	if _, found, err := s.currentDungeonCycleRequirementForDungeon(runtime.Dungeon.ID); err != nil {
		return 0, err
	} else if found {
		return 3, nil
	}
	if runtime.Dungeon.Metadata.Flags[currentDimensionGateDungeonPVFFlag] {
		return 7, nil
	}
	switch runtime.Dungeon.ID {
	// Both PVF Epic Road editions, including the zero-fatigue Silent Forest.
	case 6004, 6013, 6062, 6063, 6064, 6065:
		return 8, nil
	}
	return 0, nil
}

// Called only for an authoritative retired abyss-wave monster, never a pillar,
// a regular room monster, an unannounced actor, or a client-supplied task ID.
func (s *Service) persistPrivateActivityHellKill(ctx context.Context, session *gameSession, runtime *runtimeDungeonState, objectKey uint32, fresh bool) error {
	if session == nil || session.selectedCharacterID == 0 || runtime == nil || runtime.Room == nil || runtime.Session == nil || session.dungeon.runtime != runtime || !currentDungeonHellPartyAbyssRun(runtime) {
		return nil
	}
	scene, ok := runtime.Session.Scene()
	if !ok || !currentHellPartySceneIsHellRoom(runtime, scene) {
		return nil
	}
	for _, actor := range runtime.Room.Snapshot().ExtendedActors {
		if actor.ObjectKey == objectKey && actor.Kind == runtimeDungeonActorSpecialMonster && actor.HellPartyGroupID > 0 && actor.State == runtimeDungeonMonsterDefeated {
			now := s.gameplayNow()
			if runtime.privateActivityHellDeaths == nil {
				runtime.privateActivityHellDeaths = map[uint32]time.Time{}
			}
			at := runtime.privateActivityHellDeaths[objectKey]
			if at.IsZero() {
				if !fresh {
					return nil
				}
				at = now
				runtime.privateActivityHellDeaths[objectKey] = at
			}
			return s.persistPrivateActivity(ctx, s.accountIDForSession(session), strconv.Itoa(int(session.selectedCharacterID)), 1, now, at)
		}
	}
	return nil
}
func (s *Service) persistPrivateActivityDungeon(ctx context.Context, session *gameSession, runtime *runtimeDungeonState) error {
	c, err := s.privateActivityCatalog()
	if err != nil || c == nil {
		return err
	}
	id, err := s.privateActivityDungeonTask(runtime)
	if err != nil || id == 0 {
		return err
	}
	snapshot := runtime.Session.Snapshot()
	if err := ensureCurrentDungeonCompletionReceiptKey(session, runtime, snapshot, s.gameplayNow(), "private_activity"); err != nil {
		return err
	}
	return s.persistPrivateActivity(ctx, s.accountIDForSession(session), strconv.Itoa(int(session.selectedCharacterID)), id, s.gameplayNow(), runtime.clearMapCompletionAt)
}
func privateActivityFinalRaid(f *currentAntonRewardFlow) bool {
	return f != nil && !f.Single && f.FiendWarRetryCount == 0 && ((f.Mode == 1 || f.Mode == 2) && f.Phase == 1 || f.Mode == 3 && f.Phase == 0)
}
func (s *Service) settlePrivateActivity(ctx context.Context, session *gameSession, claim uint16, day, fingerprint int64, generation uint64) (activeness.Snapshot, error) {
	if session == nil || session.selectedCharacterID == 0 || session.loginRevoked.Load() {
		return activeness.Snapshot{}, activeness.ErrOwner
	}
	character := session.selectedCharacterID
	currentGeneration := session.characterGeneration
	account := s.accountIDForSession(session)
	if claim > 0 && generation != currentGeneration {
		return activeness.Snapshot{}, activeness.ErrOwner
	}
	now := s.gameplayNow()
	validate := func() error {
		if session.loginRevoked.Load() || session.selectedCharacterID != character || session.characterGeneration != currentGeneration || s.accountIDForSession(session) != account {
			return activeness.ErrOwner
		}
		if activeness.Day(s.gameplayNow()) != activeness.Day(now) {
			return activeness.ErrStaleDay
		}
		return nil
	}
	c, err := s.privateActivityCatalog()
	if err != nil {
		return activeness.Snapshot{}, err
	}
	if c == nil {
		return activeness.Snapshot{}, activeness.ErrCatalog
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return activeness.Snapshot{}, err
	}
	g, ok := s.repositoryGroup()
	if !ok {
		return activeness.Snapshot{}, activeness.ErrOwner
	}
	key := strconv.Itoa(int(character))
	return activeness.Settle(ctx, g, c, activeness.Command{AccountID: account, CharacterID: key, Now: now, Claim: claim, ExpectedDay: day, ExpectedFingerprint: fingerprint, Validate: validate,
		Deliver: func(ctx context.Context, boxes repo.MailboxRepository, r activeness.Reward, receipt string) (string, error) {
			rows := [][2]uint32{{r.PotionID, r.PotionCount}}
			if r.InvitationCount > 0 {
				rows = append(rows, [2]uint32{r.InvitationID, r.InvitationCount})
			}
			attachments := make([]repo.MailAttachment, 0, len(rows))
			for i, row := range rows {
				def, err := catalog.ResolveItem(row[0])
				if err != nil {
					return "", err
				}
				def, err = currentPVFItemDefinitionForGrantAt(def, now)
				if err != nil {
					return "", err
				}
				if def.Kind != dungeonDropItemStackable || (!def.ExpirationDate.IsZero() && !def.ExpirationDate.After(now)) || (i == 0 && def.AttachType != "[account]") {
					return "", fmt.Errorf("invalid activity reward %d", row[0])
				}
				attachments = append(attachments, repo.MailAttachment{ItemID: int64(row[0]), Count: int64(row[1]), ExpireAt: def.ExpirationDate})
			}
			return repo.AppendSystemMail(ctx, boxes, repo.SystemMailDelivery{RecipientCharacterID: key, Title: fmt.Sprintf("每日活跃度%d点奖励", r.Required), Body: fmt.Sprintf("%d欢乐代币券已计入商城余额，请查收疲劳药和邀请函。", r.HappyTokens), Source: "private_activity", CompletionKey: receipt, CreatedAt: now, Attachments: attachments})
		},
	})
}
