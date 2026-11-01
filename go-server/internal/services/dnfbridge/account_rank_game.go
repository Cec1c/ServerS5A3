package dnfbridge

import (
	"context"
	"encoding/binary"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf16"

	"longheng.io/server/internal/modules/dnf/activityrank"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/eventloop"
)

// 与个人统计/战令分开路由，仅提供当前认证账号可用的只读榜单。
const accountRankGameOpcode uint16 = 0x7f12

type gameAccountRankState struct {
	busy    atomic.Bool
	last    time.Time
	request uint32
}
type gameAccountRankRequest struct {
	gamePlayerStatsRequest
	metric, period, mode byte
	offset               uint32
	named                bool
}

func decodeAccountRankRequest(b []byte) (gameAccountRankRequest, bool) {
	if len(b) != 20 || (string(b[:4]) != "RKQ1" && string(b[:4]) != "RKQ2") || b[12] > 4 || b[13] > 2 || b[14] > 1 || b[15] != 0 {
		return gameAccountRankRequest{}, false
	}
	r := gameAccountRankRequest{gamePlayerStatsRequest: gamePlayerStatsRequest{binary.LittleEndian.Uint32(b[4:8]), binary.LittleEndian.Uint32(b[8:12])}, metric: b[12], period: b[13], mode: b[14], offset: binary.LittleEndian.Uint32(b[16:20]), named: string(b[:4]) == "RKQ2"}
	return r, r.id != 0 && r.epoch != 0 && r.offset <= 100000 && r.offset%5 == 0
}

type accountRankGameRow struct {
	rank, characters uint32
	score            int64
	name             string
	adventure        string
}

// 固定UTF-16字段；角色名不能引入控制字符或原生富文本标签，也不回传登录账号。
func putAccountRankName(dst []byte, name string) { putAccountRankText(dst, name, "历史角色") }
func putAccountRankText(dst []byte, name, fallback string) {
	units := make([]uint16, 0, 31)
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '<' || r == '>' {
			continue
		}
		v := utf16.Encode([]rune{r})
		if len(units)+len(v) > 31 {
			break
		}
		units = append(units, v...)
	}
	if len(units) == 0 {
		units = utf16.Encode([]rune(fallback))
	}
	for i, u := range units {
		binary.LittleEndian.PutUint16(dst[i*2:i*2+2], u)
	}
}

func (s *Service) buildAccountRankGame(ctx context.Context, identity localPlayerIdentity, r gameAccountRankRequest, now time.Time) ([]byte, error) {
	ranks, err := s.currentActivityRanks()
	if err != nil {
		return nil, err
	}
	metrics := []string{repo.ActivityFatigue, repo.ActivityEpic, repo.ActivityRecommendedDungeon, repo.ActivityColorless, repo.ActivityGoldSpent}
	periods := []string{"day", "week", "all"}
	q := activityrank.Query{Metric: metrics[r.metric], Period: periods[r.period], AccountID: identity.AccountID, Offset: int(r.offset), Limit: 5}
	if r.mode == 1 {
		q.Offset = 0
	}
	board, err := ranks.Board(ctx, q, now)
	if err != nil {
		return nil, err
	}
	total, offset := board.TotalAccounts, int(r.offset)
	if r.mode == 1 {
		total = 0
		if board.Self != nil {
			total = len(board.Self.Characters)
		}
	}
	if total == 0 {
		offset = 0
	} else if offset >= total {
		offset = (total - 1) / 5 * 5
	}
	if r.mode == 0 && offset != q.Offset {
		q.Offset = offset
		board, err = ranks.Board(ctx, q, now)
		if err != nil {
			return nil, err
		}
	}
	group, ok := s.repositoryGroup()
	if !ok || group.Character == nil {
		return nil, repo.ErrActivityTransaction
	}
	name := func(account, id string) (string, error) {
		ch, found, e := group.Character.Load(ctx, id)
		if e != nil {
			return "", e
		}
		if !found || ch.AccountID != account {
			return "历史角色", nil
		}
		return ch.Name, nil
	}
	adventures := map[string]string{}
	adventure := func(account string) (string, error) {
		if !r.named {
			return "", nil
		}
		if n, ok := adventures[account]; ok {
			return n, nil
		}
		if group.Account == nil {
			return "", repo.ErrActivityTransaction
		}
		a, found, err := group.Account.Load(ctx, account)
		if err != nil {
			return "", err
		}
		n := "未命名冒险团"
		if found && a.AccountID == account && strings.TrimSpace(a.RepresentAccountName) != "" {
			n = a.RepresentAccountName
		}
		adventures[account] = n
		return n, nil
	}
	selfAdventure, err := adventure(identity.AccountID)
	if err != nil {
		return nil, err
	}
	rows := make([]accountRankGameRow, 0, 5)
	if r.mode == 0 {
		for _, entry := range board.Entries {
			if len(entry.Characters) == 0 {
				return nil, repo.ErrActivityRank
			}
			representative := entry.Characters[0]
			for _, ch := range entry.Characters[1:] {
				if ch.Score > representative.Score {
					representative = ch
				}
			}
			n, e := name(entry.AccountID, representative.CharacterID)
			if e != nil {
				return nil, e
			}
			a, err := adventure(entry.AccountID)
			if err != nil {
				return nil, err
			}
			rows = append(rows, accountRankGameRow{rank: uint32(entry.Rank), characters: uint32(len(entry.Characters)), score: entry.Score, name: n, adventure: a})
		}
	} else if board.Self != nil {
		characters := board.Self.Characters
		sort.SliceStable(characters, func(i, j int) bool { return characters[i].Score > characters[j].Score })
		for i := offset; i < min(offset+5, len(characters)); i++ {
			n, e := name(identity.AccountID, characters[i].CharacterID)
			if e != nil {
				return nil, e
			}
			rows = append(rows, accountRankGameRow{rank: uint32(i + 1), characters: 1, score: characters[i].Score, name: n, adventure: selfAdventure})
		}
	}
	headerSize, rowSize := 64, 80
	if r.named {
		headerSize, rowSize = 128, 144
	}
	b := make([]byte, headerSize+rowSize*len(rows))
	copy(b, "RKA1")
	if r.named {
		copy(b, "RKA2")
		putAccountRankText(b[64:128], selfAdventure, "未命名冒险团")
	}
	b[4], b[5], b[6], b[7] = r.metric, r.period, r.mode, byte(len(rows))
	binary.LittleEndian.PutUint32(b[8:12], uint32(total))
	binary.LittleEndian.PutUint32(b[12:16], uint32(offset))
	if board.Self != nil {
		binary.LittleEndian.PutUint32(b[16:20], uint32(board.Self.Rank))
		binary.LittleEndian.PutUint64(b[20:28], uint64(board.Self.Score))
		binary.LittleEndian.PutUint32(b[56:60], uint32(len(board.Self.Characters)))
	}
	binary.LittleEndian.PutUint64(b[28:36], uint64(board.GeneratedAt.Unix()))
	copy(b[36:46], board.Window.FromDay)
	copy(b[46:56], board.Window.ThroughDay)
	for i, row := range rows {
		p := b[headerSize+i*rowSize : headerSize+(i+1)*rowSize]
		binary.LittleEndian.PutUint32(p[:4], row.rank)
		binary.LittleEndian.PutUint32(p[4:8], row.characters)
		binary.LittleEndian.PutUint64(p[8:16], uint64(row.score))
		putAccountRankName(p[16:80], row.name)
		if r.named {
			putAccountRankText(p[80:144], row.adventure, "未命名冒险团")
		}
	}
	return b, nil
}

func (s *Service) sendAccountRankGame(session *gameSession, r gameAccountRankRequest, status uint16, data []byte) error {
	limit := 464
	if r.named {
		limit = 848
	}
	if len(data) > limit {
		status = 5
		data = nil
	}
	b := encodeGamePlayerStatsReply(r.gamePlayerStatsRequest, status, data)
	copy(b, "RKR1")
	return s.sendGameUpperFixed16Transport(session, accountRankGameOpcode, b, 1, 0, false, "account_rank_v1_fixed16_plain")
}

func (s *Service) handleAccountRankGame(session *gameSession, body []byte) error {
	r, ok := decodeAccountRankRequest(body)
	if !ok || session == nil {
		return nil
	}
	identity, bound, ok := s.playerStatsGameIdentity(session)
	if !ok {
		return nil
	}
	state := &session.accountRank
	now := time.Now()
	if !state.last.IsZero() && now.Sub(state.last) < time.Second {
		return nil
	}
	state.last = now
	state.request = r.id
	if !state.busy.CompareAndSwap(false, true) {
		return s.sendAccountRankGame(session, r, 2, nil)
	}
	// 与个人统计共用四个后台查询名额，不在游戏事件循环查询或排序。
	if !s.playerStatsQueries.acquire() {
		state.busy.Store(false)
		return s.sendAccountRankGame(session, r, 2, nil)
	}
	events := session.events
	go func() {
		defer state.busy.Store(false)
		defer s.playerStatsQueries.release()
		ctx, cancel := context.WithTimeout(session.ctx, 3*time.Second)
		defer cancel()
		data, err := s.buildAccountRankGame(ctx, identity, r, s.gameplayNow())
		status := uint16(0)
		if err != nil {
			status = 4
			data = nil
		}
		if session.ctx.Err() != nil || session.loginRevoked.Load() || !events.active.Load() {
			return
		}
		_ = events.loop.Submit(eventloop.Event{Type: "account-rank-result", Priority: eventloop.PriorityLow, Context: session.ctx, Data: gameSessionEventTask{
			source: "account-rank-result", characterID: bound.character, characterGeneration: bound.generation, async: true,
			run: func() error {
				after, _, valid := s.playerStatsGameIdentity(session)
				if !valid || after != identity || !s.boundGameSessionCharacterCurrent(bound) || state.request != r.id {
					return nil
				}
				return s.sendAccountRankGame(session, r, status, data)
			},
		}})
	}()
	return nil
}
