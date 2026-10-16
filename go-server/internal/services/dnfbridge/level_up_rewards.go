package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const currentLevelUpRewardClockName = "character:level-up-rewards"
const currentLevelUpRewardRetryDelay = 30 * time.Second
const currentLevelUpRewardBroadcastOpcode uint16 = 86

type levelUpRewardRuntime struct {
	mu         sync.Mutex
	opMu       sync.Mutex
	loaded     bool
	catalog    *progression.LevelUpRewardCatalog
	catalogErr error
	retryAfter map[string]time.Time
	cursor     string
}

func (s *Service) currentLevelUpRewardCatalog(ctx context.Context) (*progression.LevelUpRewardCatalog, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	r := &s.levelUpRewards
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !r.loaded {
		var catalog *progression.LevelUpRewardCatalog
		s.initialEquipmentMu.Lock()
		archive, err := s.initialEquipmentArchiveLocked()
		s.initialEquipmentMu.Unlock()
		if err == nil {
			catalog, err = progression.LoadLevelUpRewards(ctx, archive)
		}
		// 请求取消或超时不属于资源错误，后续升级应能重新读取PVF。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		// 与86版一样：本进程固定现用PVF配置，读错保留事件，修正资源后重启重试。
		r.catalog, r.catalogErr, r.loaded = catalog, err, true
	}
	return r.catalog, r.catalogErr
}

// 现用EXE 02027CC0 / subtype6 -> 020286D1：只读u8(6)+DSTR姓名。
// 02028701固定Lv85、文本48012固定500张；不能拿它播报其它等级或数量。
func buildCurrentLevelUpRewardBroadcast(delivery progression.LevelUpRewardDelivery) ([]byte, bool) {
	if delivery.Level != 85 || delivery.ItemID != 3330 || delivery.Count != 500 || strings.TrimSpace(delivery.CharacterName) == "" {
		return nil, false
	}
	for _, r := range delivery.CharacterName {
		if unicode.IsControl(r) {
			return nil, false
		}
	}
	name, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(delivery.CharacterName))
	if err != nil || len(name) == 0 || len(name) >= 256 {
		return nil, false
	}
	var writer packetWriter
	writer.writeByte(6)
	writer.writeRawDstr(name)
	return writer.bytes(), true
}

func (s *Service) processCurrentLevelUpRewards(parent context.Context, characterID string) {
	if s == nil || s.options.currentClientVersion != "official-24.9" || characterID == "" {
		return
	}
	// 命令尾与恢复线程可以竞争，同一事务的永久记录仍是最终防重依据。
	if !s.levelUpRewards.opMu.TryLock() {
		return
	}
	defer s.levelUpRewards.opMu.Unlock()
	now := s.gameplayNow().UTC()
	s.levelUpRewards.mu.Lock()
	retry := s.levelUpRewards.retryAfter[characterID]
	s.levelUpRewards.mu.Unlock()
	if now.Before(retry) {
		return
	}
	repos, available := s.repositoryGroup()
	if !available || repos.Character == nil || repos.MailboxAssets == nil {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, createWriteTimeout)
	defer cancel()
	character, found, err := repos.Character.Load(ctx, characterID)
	if err == nil && (!found || !dnfrepo.HasPendingLevelUp(character)) {
		return
	}
	var identity boundGameSessionCharacter
	if numeric, parseErr := strconv.ParseUint(characterID, 10, 16); parseErr == nil {
		identity, _ = s.onlineGameSessionCharacterSnapshot(uint16(numeric))
	}
	var deliveries []progression.LevelUpRewardDelivery
	if err == nil {
		var catalog *progression.LevelUpRewardCatalog
		catalog, err = s.currentLevelUpRewardCatalog(ctx)
		if err == nil {
			deliveries, err = progression.DeliverLevelUpRewards(ctx, repos, catalog, progression.LevelUpRewardCommand{CharacterID: characterID, Now: now, ResolveAttachment: func(reward progression.LevelUpReward) (dnfrepo.MailAttachment, error) {
				items, e := s.currentPVFItemCatalog()
				if e != nil {
					return dnfrepo.MailAttachment{}, e
				}
				definition, e := items.ResolveItem(uint32(reward.ItemID))
				if e != nil {
					return dnfrepo.MailAttachment{}, e
				}
				if definition.Kind != dungeonDropItemStackable {
					return dnfrepo.MailAttachment{}, fmt.Errorf("升级奖励物品%d不支持邮件附件类型", reward.ItemID)
				}
				definition, e = currentPVFItemDefinitionForGrantAt(definition, now)
				if e != nil {
					return dnfrepo.MailAttachment{}, e
				}
				return dnfrepo.MailAttachment{ItemID: reward.ItemID, Count: reward.Count, ExpireAt: definition.ExpirationDate}, nil
			}})
		}
	}
	s.levelUpRewards.mu.Lock()
	if err != nil {
		if s.levelUpRewards.retryAfter == nil {
			s.levelUpRewards.retryAfter = make(map[string]time.Time)
		}
		s.levelUpRewards.retryAfter[characterID] = now.Add(currentLevelUpRewardRetryDelay)
	} else {
		delete(s.levelUpRewards.retryAfter, characterID)
	}
	s.levelUpRewards.mu.Unlock()
	if err != nil {
		s.logWarn("升级奖励暂缓发放，已保留升级事件等待重试", "character_id", characterID, "error", err)
		return
	}
	if len(deliveries) > 0 {
		s.notifyCurrentLevelUpRewards(ctx, identity, deliveries)
	}
}

func (s *Service) notifyCurrentLevelUpRewards(ctx context.Context, identity boundGameSessionCharacter, deliveries []progression.LevelUpRewardDelivery) {
	if len(deliveries) == 0 || !s.boundGameSessionCharacterCurrent(identity) {
		return
	}
	s.mu.Lock()
	account := strings.TrimSpace(identity.session.accountID)
	s.mu.Unlock()
	reservation, err := s.reserveCurrentCharacterAssets(ctx, identity, account)
	if err != nil {
		return
	}
	// 邮件已经提交，投影失败不会回滚或再发邮件。离线者下次登录读取权威提醒。
	if err := s.sendMailboxAlarmForSession(identity.session, identity.character, "升级奖励邮件已提交"); err != nil {
		s.logWarn("升级奖励邮件已保存，在线提醒发送失败", "character_id", identity.character, "error", err)
	}
	s.releaseCurrentCharacterAssets(reservation)
	for _, delivery := range deliveries {
		body, valid := buildCurrentLevelUpRewardBroadcast(delivery)
		if !valid {
			continue
		}
		s.mu.Lock()
		recipients := make([]boundGameSessionCharacter, 0, len(s.gameSessions))
		for id, session := range s.gameSessions {
			if session != nil && session.selectedCharacterID == id {
				recipients = append(recipients, boundGameSessionCharacter{session: session, character: id, generation: session.characterGeneration})
			}
		}
		s.mu.Unlock()
		sort.Slice(recipients, func(i, j int) bool { return recipients[i].character < recipients[j].character })
		// 不持有来源角色租约再获取其它角色租约，避免跨角色锁环。
		for _, recipient := range recipients {
			s.mu.Lock()
			account := strings.TrimSpace(recipient.session.accountID)
			s.mu.Unlock()
			reservation, e := s.reserveCurrentCharacterAssets(ctx, recipient, account)
			if e != nil {
				continue
			}
			e = s.sendGameUpperRawClass(recipient.session, currentLevelUpRewardBroadcastOpcode, body, 0)
			s.releaseCurrentCharacterAssets(reservation)
			if e != nil {
				s.logWarn("升级奖励已保存，原生播报发送失败", "character_id", recipient.character, "error", e)
			}
		}
	}
}

// 统一命令尾覆盖经验发送者及队友。奖励事实来自仓储，不以客户端发来的等级认领。
func (s *Service) afterCurrentLevelUpRewardCommand(session *gameSession) {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return
	}
	ids := map[uint16]bool{identity.character: true}
	if manager := s.runtimePartyManagerForService(); manager != nil {
		if party, found := manager.SnapshotByUser(identity.character, 0); found {
			for _, member := range party.Members {
				ids[member.UserID] = true
			}
		}
	}
	for id := range ids {
		if id != 0 {
			s.processCurrentLevelUpRewards(context.Background(), strconv.Itoa(int(id)))
		}
	}
}

func (s *Service) recoverCurrentLevelUpRewards(ctx context.Context) {
	repos, ok := s.repositoryGroup()
	if !ok {
		return
	}
	reader, ok := repos.Character.(dnfrepo.PendingLevelUpReader)
	if !ok {
		return
	}
	s.levelUpRewards.mu.Lock()
	cursor := s.levelUpRewards.cursor
	s.levelUpRewards.mu.Unlock()
	queryCtx, cancel := context.WithTimeout(ctx, createWriteTimeout)
	ids, err := reader.ListPendingLevelUpCharacters(queryCtx, cursor, 128)
	cancel()
	if err != nil {
		s.logWarn("恢复升级奖励事件读取失败，稍后重试", "error", err)
		return
	}
	s.levelUpRewards.mu.Lock()
	if len(ids) < 128 {
		s.levelUpRewards.cursor = ""
	} else {
		s.levelUpRewards.cursor = ids[len(ids)-1]
	}
	s.levelUpRewards.mu.Unlock()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		s.processCurrentLevelUpRewards(ctx, id)
	}
}

func (s *Service) startCurrentLevelUpRewardClock(ctx context.Context) {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return
	}
	wake := make(chan struct{}, 1)
	wake <- struct{}{}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
				s.recoverCurrentLevelUpRewards(ctx)
			}
		}
	}()
	var schedule func()
	schedule = func() {
		err := s.ensureGameplayTimeQueue().ScheduleAfter(currentLevelUpRewardClockName, currentLevelUpRewardRetryDelay, func(_ time.Time) {
			if ctx.Err() != nil {
				return
			}
			select {
			case wake <- struct{}{}:
			default:
			}
			schedule()
		})
		if err != nil {
			s.logWarn("升级奖励重试时钟调度失败", "error", err)
		}
	}
	schedule()
}
