package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	bp "longheng.io/server/internal/modules/dnf/battlepass"
	"longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/eventloop"
	"strconv"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf16"
)

type gameBattlePassState struct {
	busy    atomic.Bool
	last    time.Time
	request uint32
}

// 登录引导和查询均可重试；同角色服务日只写一次，账号读取时按日期去重。
func (s *Service) recordBattlePassLogin(session *gameSession) error {
	if session == nil || !s.currentBattlePassEnabled() {
		return nil
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return nil
	}
	if _, _, e := s.currentBattlePassCatalog(); e != nil {
		return nil
	}
	p, e := currentBattlePassPolicy()
	if e != nil {
		return e
	}
	now := s.gameplayNow()
	if !p.Active(now) {
		return nil
	}
	g, ok := s.repositoryGroup()
	if !ok || g.CharacterAssets == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(session.ctx, createWriteTimeout)
	defer cancel()
	id := strconv.Itoa(int(identity.character))
	account := s.accountIDForSession(session)
	return g.CharacterAssets.WithinCharacterAssets(ctx, id, func(chars repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository) error {
		if !s.boundGameSessionCharacterCurrent(identity) {
			return bp.ErrUnavailable
		}
		ch, found, e := chars.Load(ctx, id)
		if e != nil {
			return e
		}
		if !found || ch.AccountID != account {
			return bp.ErrUnavailable
		}
		key := fmt.Sprintf("%s%s:30:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(now))
		if ch.Stats[key] > 0 {
			return nil
		}
		ch = repo.CloneCharacter(ch)
		repo.AddBattlePassCounter(&ch, 30, -1, 1, now)
		return repo.SaveCharacterFields(ctx, chars, ch, repo.CharacterFieldStats)
	})
}

type gameBattlePassRequest struct {
	gamePlayerStatsRequest
	action, category, slot    byte
	season, day, week, target uint32
	quoteHappy, quoteToken    uint32
	compact                   bool
	catalogDigest             [32]byte
}

func decodeBattlePassGame(b []byte) (gameBattlePassRequest, bool) {
	var digest [32]byte
	compact := len(b) == 64 && string(b[:4]) == "BPQ3"
	if compact {
		copy(digest[:], b[32:])
		b = append([]byte(nil), b[:32]...)
		copy(b, "BPQ2")
	}
	if len(b) != 32 || string(b[:4]) != "BPQ2" || b[12] > 4 || b[15] != 0 || (b[12] != 4 && binary.LittleEndian.Uint32(b[28:]) != 0) {
		return gameBattlePassRequest{}, false
	}
	r := gameBattlePassRequest{gamePlayerStatsRequest: gamePlayerStatsRequest{binary.LittleEndian.Uint32(b[4:8]), binary.LittleEndian.Uint32(b[8:12])}, action: b[12], category: b[13], slot: b[14], season: binary.LittleEndian.Uint32(b[16:20]), day: binary.LittleEndian.Uint32(b[20:24])}
	r.compact, r.catalogDigest = compact, digest
	// 周期以day字段为准；week使用同一服务日计算，target放在24，类别/槽位均有界。
	r.week = r.day - (r.day+3)%7
	r.target = binary.LittleEndian.Uint32(b[24:28])
	if r.id == 0 || r.epoch == 0 {
		return r, false
	}
	if r.action == 0 {
		return r, r.category == 0 && r.slot == 0 && r.season == 0 && r.day == 0 && r.target == 0
	}
	if r.action == 1 {
		return r, r.category <= 2 && (r.category < 2 && r.slot < 4 || r.category == 2 && r.slot < 18) && r.target > 0 && r.target <= 65535
	}
	if r.action == 2 {
		return r, r.category < 2 && r.slot == 0 && r.target >= 1 && r.target <= 80
	}
	if r.action == 4 {
		quote := binary.LittleEndian.Uint32(b[28:])
		r.quoteHappy, r.quoteToken = quote&0x3fff, (quote>>14)&0x3fff
		return r, r.category == 0 && r.slot == 0 && r.target == bp.PremiumPrice && quote>>28 == 0 && r.quoteHappy+r.quoteToken <= bp.PremiumPrice
	}
	return r, r.category == 0 && r.slot == 0 && r.target == 0
}
func currentBattlePassPolicy() (bp.Policy, error) {
	start, e := time.Parse(time.RFC3339, envString("DNFBRIDGE_BATTLEPASS_START", "2026-10-29T06:00:00+08:00"))
	if e != nil {
		return bp.Policy{}, e
	}
	id, e := strconv.ParseUint(envString("DNFBRIDGE_BATTLEPASS_SEASON", "20261029"), 10, 32)
	if e != nil {
		return bp.Policy{}, e
	}
	p := bp.Policy{ID: uint32(id), Start: start, End: start.AddDate(0, 0, 100)}
	return p, p.Validate()
}
func (s *Service) currentBattlePassCatalog() (*bp.Catalog, string, error) {
	s.battlePassCatalogMu.Lock()
	defer s.battlePassCatalogMu.Unlock()
	if s.battlePassCatalog != nil {
		return s.battlePassCatalog, s.battlePassFingerprint, nil
	}
	s.initialEquipmentMu.Lock()
	a := s.initialEquipmentArchive
	s.initialEquipmentMu.Unlock()
	if a == nil {
		return nil, "", bp.ErrUnavailable
	}
	text, e := a.ReadText(bp.Path)
	if e != nil {
		return nil, "", e
	}
	c, e := bp.Parse(text)
	if e != nil {
		return nil, "", e
	}
	items, e := s.currentPVFItemCatalog()
	if e != nil {
		return nil, "", e
	}
	seen := map[uint32]bool{}
	for _, track := range c.Rewards {
		for _, rs := range track {
			for _, r := range rs {
				if seen[r.ItemID] {
					continue
				}
				if _, e = items.ResolveItem(r.ItemID); e != nil {
					return nil, "", e
				}
				seen[r.ItemID] = true
			}
		}
	}
	h := sha256.Sum256([]byte(text))
	fingerprint := hex.EncodeToString(h[:])
	s.battlePassCatalog, s.battlePassFingerprint = c, fingerprint
	return c, fingerprint, nil
}
func putBattlePassText(dst []byte, text string) {
	at := 0
	for _, r := range text {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '<' || r == '>' {
			continue
		}
		units := utf16.Encode([]rune{r})
		if at+len(units)*2 > len(dst)-2 {
			break
		}
		for _, u := range units {
			binary.LittleEndian.PutUint16(dst[at:], u)
			at += 2
		}
	}
}
func encodeBattlePassSnapshot(c *bp.Catalog, v bp.Snapshot) []byte {
	// BPA5增加两种代币券余额；同一内部连接，奖励和任务字段保持。
	b := make([]byte, 96+26*144+160*40)
	copy(b, "BPA5")
	put := func(off int, n uint32) { binary.LittleEndian.PutUint32(b[off:off+4], n) }
	put(4, v.Policy.ID)
	put(8, bp.DayNumber(v.Policy.Start))
	put(12, v.State.Day)
	put(16, v.State.Week)
	put(20, v.State.Level)
	put(24, v.State.XP)
	put(28, c.Experience[v.State.Level])
	binary.LittleEndian.PutUint64(b[32:40], uint64(v.Policy.End.Unix()))
	put(40, 1)
	if v.State.Premium {
		put(40, 3)
	}
	put(44, 26)
	put(48, 80)
	put(52, uint32(len(v.MailIDs)))
	put(56, uint32(c.Tier[v.State.Level]))
	put(60, bp.PremiumPrice)
	binary.LittleEndian.PutUint64(b[64:72], uint64(max(v.CeraBalance, 0)))
	binary.LittleEndian.PutUint64(b[72:80], uint64(max(v.TokenCeraBalance, 0)))
	binary.LittleEndian.PutUint64(b[80:88], uint64(max(v.HappyTokenCeraBalance, 0)))
	for i, m := range v.Missions {
		p := b[96+i*144 : 96+(i+1)*144]
		binary.LittleEndian.PutUint16(p, uint16(m.ID))
		binary.LittleEndian.PutUint16(p[2:], m.Flags)
		binary.LittleEndian.PutUint32(p[4:], m.Progress)
		binary.LittleEndian.PutUint32(p[8:], m.Target)
		binary.LittleEndian.PutUint32(p[12:], m.GrantedXP)
		putBattlePassText(p[16:144], m.Text)
	}
	for track := 0; track < 2; track++ {
		for level := 1; level <= 80; level++ {
			p := b[96+26*144+(track*80+level-1)*40:]
			rs := c.Rewards[track][level]
			if len(rs) > 4 {
				return nil // 不截断已配置奖励；解析入口也检查四项上限。
			}
			p[0], p[2], p[3] = byte(len(rs)), byte(level), byte(track)
			for i, reward := range rs {
				binary.LittleEndian.PutUint32(p[8+i*8:], reward.ItemID)
				binary.LittleEndian.PutUint32(p[12+i*8:], reward.Count)
			}
			if track == 1 && !v.State.Premium {
				p[1] |= 4
			}
			if bp.Claimed(v.State, track, level) {
				p[1] |= 2
			} else if p[1]&4 == 0 && level <= int(v.State.Level) && len(rs) > 0 {
				p[1] |= 1
			}
		}
	}
	return b
}
func (s *Service) settleBattlePassGame(ctx context.Context, session *gameSession, identity localPlayerIdentity, bound boundGameSessionCharacter, r gameBattlePassRequest) ([]byte, bool, error) {
	if !s.currentBattlePassEnabled() {
		return nil, false, bp.ErrUnavailable
	}
	c, fingerprint, e := s.currentBattlePassCatalog()
	if e != nil {
		return nil, false, e
	}
	if r.compact && r.catalogDigest != c.WireDigest() {
		return nil, false, errBattlePassCatalogMismatch
	}
	policy, e := currentBattlePassPolicy()
	if e != nil {
		return nil, false, e
	}
	now := s.gameplayNow()
	if !policy.Active(now) {
		return nil, false, bp.ErrUnavailable
	}
	g, ok := s.repositoryGroup()
	if !ok || g.Character == nil {
		return nil, false, bp.ErrUnavailable
	}
	counters, e := loadBattlePassCounters(ctx, g, identity.AccountID, policy, now)
	if e != nil {
		return nil, false, e
	}
	id := strconv.Itoa(int(bound.character))
	deliver := s.currentPortedMailDelivery(ctx, session, id, "阿拉德探险记奖励", "战令等级奖励已发放，请查收附件。", "battlepass_v2")
	v, e := bp.Execute(ctx, g, c, policy, bp.Command{AccountID: identity.AccountID, CharacterID: id, Now: now, Action: r.action, Category: r.category, Slot: r.slot, Season: r.season, Day: r.day, Week: r.week, Target: r.target, Counters: counters, Fingerprint: fingerprint,
		QuoteHappy: r.quoteHappy, QuoteToken: r.quoteToken,
		Validate: func() bool {
			return s.currentBattlePassEnabled() && !session.loginRevoked.Load() && s.boundGameSessionCharacterCurrent(bound)
		},
		Deliver: func(box repo.MailboxRepository, rs []bp.Reward, key string) (string, error) {
			last := ""
			for i, reward := range rs {
				var err error
				last, err = deliver(box, onlineevent.AttendancePVFItemReward{ItemID: int64(reward.ItemID), Count: int64(reward.Count)}, fmt.Sprintf("%s:%d", key, i))
				if err != nil {
					return "", err
				}
			}
			return last, nil
		},
	})
	if e != nil {
		return nil, false, e
	}
	var data []byte
	if r.compact {
		data = encodeBattlePassCompactSnapshot(c, v)
	} else {
		data = encodeBattlePassSnapshot(c, v)
	}
	if data == nil {
		return nil, false, fmt.Errorf("战令展示奖励超出协议容量")
	}
	return data, len(v.MailIDs) > 0, nil
}
func (s *Service) sendBattlePassGame(session *gameSession, r gameBattlePassRequest, status uint16, data []byte) error {
	if len(data) > 16384 {
		status = 5
		data = nil
	}
	b := encodeGamePlayerStatsReply(r.gamePlayerStatsRequest, status, data)
	copy(b, "BPR2")
	if r.compact {
		copy(b, "BPR3")
	}
	return s.sendGameUpperFixed16Transport(session, battlePassGameOpcode, b, 1, 0, false, "battlepass_v2_fixed16_plain")
}
func (s *Service) handleBattlePassGame(session *gameSession, body []byte) error {
	r, ok := decodeBattlePassGame(body)
	if !ok || session == nil {
		return nil
	}
	identity, bound, ok := s.playerStatsGameIdentity(session)
	if !ok {
		return nil
	}
	state := &session.battlePassBusiness
	now := time.Now()
	if !state.last.IsZero() && now.Sub(state.last) < time.Second {
		return s.sendBattlePassGame(session, r, 2, nil)
	}
	state.last, state.request = now, r.id
	if !state.busy.CompareAndSwap(false, true) {
		return s.sendBattlePassGame(session, r, 2, nil)
	}
	if !s.playerStatsQueries.acquire() {
		state.busy.Store(false)
		return s.sendBattlePassGame(session, r, 2, nil)
	}
	events := session.events
	go func() {
		defer state.busy.Store(false)
		defer s.playerStatsQueries.release()
		ctx, cancel := context.WithTimeout(session.ctx, 5*time.Second)
		defer cancel()
		data, mail, e := s.settleBattlePassGame(ctx, session, identity, bound, r)
		status := uint16(0)
		if e != nil {
			status = 4
			if errors.Is(e, bp.ErrClaim) {
				status = 3
			} else if errors.Is(e, bp.ErrPeriod) {
				status = 6
			} else if errors.Is(e, bp.ErrUnavailable) {
				status = 1
			} else if errors.Is(e, bp.ErrFunds) {
				status = 7
			} else if errors.Is(e, bp.ErrPremium) {
				status = 8
			} else if errors.Is(e, bp.ErrQuote) {
				status = 9
			} else if errors.Is(e, errBattlePassCatalogMismatch) {
				status = 10
			}
			data = nil
			s.logGameEvent(session, "战令请求未完成", "操作", r.action, "任务或等级", r.target, "error", e)
		}
		if session.ctx.Err() != nil || session.loginRevoked.Load() || !events.active.Load() {
			return
		}
		_ = events.loop.Submit(eventloop.Event{Type: "battlepass-result", Priority: eventloop.PriorityLow, Context: session.ctx, Data: gameSessionEventTask{source: "battlepass-result", characterID: bound.character, characterGeneration: bound.generation, async: true, run: func() error {
			after, _, valid := s.playerStatsGameIdentity(session)
			if !valid || after != identity || !s.boundGameSessionCharacterCurrent(bound) || state.request != r.id {
				return nil
			}
			if err := s.sendBattlePassGame(session, r, status, data); err != nil {
				return err
			}
			if r.action == 4 && status == 0 {
				if err := s.sendCurrentAccountWallet(session); err != nil {
					return err
				}
			}
			if mail {
				return s.sendMailboxAlarmForSession(session, bound.character, "battlepass_v2")
			}
			return nil
		}}})
	}()
	return nil
}

func loadBattlePassCounters(ctx context.Context, g repo.Group, account string, policy bp.Policy, now time.Time) ([]bp.Counter, error) {
	chars, e := g.Character.ListByAccount(ctx, account, repo.DefaultCharacterSlotLimit)
	if e != nil {
		return nil, e
	}
	for _, ch := range chars {
		if ch.AccountID != account {
			return nil, bp.ErrUnavailable
		}
	}
	counters := bp.Counters(chars, repo.ActivityDay(policy.Start), repo.ActivityDay(now))
	fatigue, e := loadBattlePassFatigueCounters(ctx, g, account, policy, now)
	if e != nil {
		return nil, e
	}
	return append(counters, fatigue...), nil
}
func loadBattlePassFatigueCounters(ctx context.Context, g repo.Group, account string, policy bp.Policy, now time.Time) ([]bp.Counter, error) {
	var counters []bp.Counter
	today := repo.ActivityDay(now)
	week := time.Unix(int64(bp.WeekNumber(now))*86400, 0).UTC().Format("2006-01-02")
	if g.Activity != nil {
		for i, from := range []string{today, max(week, repo.ActivityDay(policy.Start)), repo.ActivityDay(policy.Start)} {
			sums, err := g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: account, FromDay: from, ThroughDay: today})
			if err != nil {
				return nil, err
			}
			for _, m := range []struct {
				kind   uint32
				metric string
			}{{3, repo.ActivityFatigue}} {
				counters = append(counters, bp.Counter{Day: today, Kind: m.kind, Param: -1, Amount: sums.Metrics[m.metric], Period: uint8(i + 1)})
			}
		}
	}
	return counters, nil
}
