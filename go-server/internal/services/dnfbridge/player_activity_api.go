package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// 玩家统计仅面向本机已用启动器令牌确认的DNF进程；不按IP、查询参数或共享账号猜身份。
type localPlayerIdentity struct {
	AccountID   string
	CharacterID string
	Generation  uint64
	ConfirmedAt time.Time
}

type playerActivityRole struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Deleted     bool             `json:"deleted"`
	Metrics     map[string]int64 `json:"metrics"`
	EpicSources map[string]int64 `json:"epic_sources"`
}

type playerActivityPeriod struct {
	Key         string               `json:"key"`
	Label       string               `json:"label"`
	FromDay     string               `json:"from_day,omitempty"`
	ThroughDay  string               `json:"through_day,omitempty"`
	Metrics     map[string]int64     `json:"metrics"`
	EpicSources map[string]int64     `json:"epic_sources"`
	Characters  []playerActivityRole `json:"characters"`
}

type playerActivitySnapshot struct {
	Version          int                    `json:"version"`
	AccountID        string                 `json:"account_id"`
	CurrentCharacter string                 `json:"current_character"`
	CurrentName      string                 `json:"current_name"`
	AdventureName    string                 `json:"adventure_name,omitempty"`
	UpdatedAt        time.Time              `json:"updated_at"`
	Periods          []playerActivityPeriod `json:"periods"`
}

func (s *Service) localPlayerIdentityForPID(pid uint32, createdAt, now time.Time) (localPlayerIdentity, bool) {
	if s == nil || pid == 0 || createdAt.IsZero() {
		return localPlayerIdentity{}, false
	}
	s.clientAccounts.mu.Lock()
	entry, found := s.clientAccounts.byPID[pid]
	s.clientAccounts.mu.Unlock()
	if !found || entry.tokenAccountID == "" || !entry.expiresAt.After(now) || entry.tokenConfirmedAt.Before(createdAt) {
		return localPlayerIdentity{}, false
	}
	identity := localPlayerIdentity{AccountID: entry.tokenAccountID, ConfirmedAt: entry.tokenConfirmedAt}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, session := range s.gameSessions {
		if session == nil || session.clientPID != pid || !session.accountFromToken || session.accountID != identity.AccountID ||
			session.selectedCharacterID != id || id == 0 || (session.ctx != nil && session.ctx.Err() != nil) {
			continue
		}
		if identity.CharacterID != "" {
			return localPlayerIdentity{}, false
		}
		identity.CharacterID = strconv.Itoa(int(id))
		identity.Generation = session.characterGeneration
	}
	return identity, identity.CharacterID != ""
}

func playerActivityDayRange(at time.Time, key string) (string, string) {
	if key == "all" {
		return "", ""
	}
	day, _ := time.Parse("2006-01-02", repo.ActivityDay(at))
	if key == "week" {
		day = day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
		return day.Format("2006-01-02"), day.AddDate(0, 0, 6).Format("2006-01-02")
	}
	return day.Format("2006-01-02"), day.Format("2006-01-02")
}

var playerActivityMetrics = [...]string{repo.ActivityFatigue, repo.ActivityAbyss, repo.ActivityEpic, repo.ActivityUnique, repo.ActivityLegendary}

func completePlayerActivityMetrics(values map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(playerActivityMetrics))
	for _, name := range playerActivityMetrics {
		out[name] = values[name]
	}
	return out
}

func (s *Service) buildPlayerActivitySnapshot(ctx context.Context, identity localPlayerIdentity, now time.Time) (playerActivitySnapshot, error) {
	return s.buildPlayerActivitySnapshotVersion(ctx, identity, now, false)
}

func (s *Service) buildPlayerActivitySnapshotVersion(ctx context.Context, identity localPlayerIdentity, now time.Time, overview bool) (playerActivitySnapshot, error) {
	group, ok := s.repositoryGroup()
	if !ok || group.Activity == nil || group.Character == nil {
		return playerActivitySnapshot{}, errors.New("账号统计尚未就绪")
	}
	characters, err := group.Character.ListByAccount(ctx, identity.AccountID, repo.DefaultCharacterSlotLimit)
	if err != nil {
		return playerActivitySnapshot{}, err
	}
	snapshot := playerActivitySnapshot{Version: 1, AccountID: identity.AccountID, CurrentCharacter: identity.CharacterID, UpdatedAt: now}
	for _, ch := range characters {
		if ch.CharacterID == identity.CharacterID {
			snapshot.CurrentName = ch.Name
		}
	}
	if snapshot.CurrentName == "" {
		return playerActivitySnapshot{}, errors.New("当前角色归属已变化")
	}

	metricValues := func(v map[string]int64) (map[string]int64, error) {
		if overview {
			return playerActivityOverviewMetrics(v)
		}
		return completePlayerActivityMetrics(v), nil
	}
	if overview {
		snapshot.AdventureName = "未命名冒险团"
		if group.Account != nil {
			a, found, e := group.Account.Load(ctx, identity.AccountID)
			if e != nil {
				return playerActivitySnapshot{}, e
			}
			if found && a.AccountID == identity.AccountID && strings.TrimSpace(a.RepresentAccountName) != "" {
				snapshot.AdventureName = a.RepresentAccountName
			}
		}
	}
	for _, definition := range [][2]string{{"today", "今日"}, {"week", "本周"}, {"all", "累计"}} {
		key, label := definition[0], definition[1]
		from, through := playerActivityDayRange(now, key)
		summary, err := group.Activity.Summary(ctx, repo.ActivityQuery{AccountID: identity.AccountID, FromDay: from, ThroughDay: through, IncludeEpicSources: true})
		if err != nil {
			return playerActivitySnapshot{}, err
		}
		periodMetrics, err := metricValues(summary.Metrics)
		if err != nil {
			return playerActivitySnapshot{}, err
		}
		period := playerActivityPeriod{Key: key, Label: label, FromDay: from, ThroughDay: through, Metrics: periodMetrics, EpicSources: playerActivityEpicSources(summary, "")}
		roles := make(map[string]playerActivityRole, len(characters)+len(summary.Characters))
		for _, ch := range characters {
			zero, _ := metricValues(nil)
			roles[ch.CharacterID] = playerActivityRole{ID: ch.CharacterID, Name: ch.Name, Metrics: zero}
		}
		for _, contribution := range summary.Characters {
			role, exists := roles[contribution.CharacterID]
			if !exists {
				role = playerActivityRole{ID: contribution.CharacterID, Name: "历史角色 " + contribution.CharacterID, Deleted: true}
			}
			role.Metrics, err = metricValues(contribution.Metrics)
			if err != nil {
				return playerActivitySnapshot{}, err
			}
			roles[role.ID] = role
		}

		if overview && key == "all" {
			period.Metrics[repo.ActivityRecommendedDungeon] = 0
			for id, role := range roles {
				role.Metrics[repo.ActivityRecommendedDungeon] = 0
				roles[id] = role
			}
			for _, ch := range characters {
				if ch.Stats["delete_flag"] != 0 {
					continue
				}
				n := ch.Stats[repo.ActivityRecommendedDungeon]
				if n < 0 || n > math.MaxInt64-period.Metrics[repo.ActivityRecommendedDungeon] {
					return playerActivitySnapshot{}, repo.ErrActivityInvalid
				}
				period.Metrics[repo.ActivityRecommendedDungeon] += n
				role := roles[ch.CharacterID]
				role.Metrics[repo.ActivityRecommendedDungeon] = n
				roles[ch.CharacterID] = role
			}
		}
		for _, role := range roles {
			role.EpicSources = playerActivityEpicSources(summary, role.ID)
			period.Characters = append(period.Characters, role)
		}
		sort.Slice(period.Characters, func(i, j int) bool {
			a, b := period.Characters[i], period.Characters[j]
			if a.Deleted != b.Deleted {
				return !a.Deleted
			}
			ai, ae := strconv.ParseUint(a.ID, 10, 64)
			bi, be := strconv.ParseUint(b.ID, 10, 64)
			if ae == nil && be == nil && ai != bi {
				return ai < bi
			}
			return a.ID < b.ID
		})
		snapshot.Periods = append(snapshot.Periods, period)
	}
	return snapshot, nil
}

// NativePlayerStatsHandler在已有回环管理监听上单独按真实TCP所属进程鉴权，不使用GM口令。
func (s *Service) NativePlayerStatsHandler(w http.ResponseWriter, r *http.Request) {
	s.serveNativePlayerStats(w, r, func(r *http.Request) (localPlayerIdentity, bool) {
		pid, created, ok := localPlayerHTTPProcess(r)
		if !ok {
			return localPlayerIdentity{}, false
		}
		return s.localPlayerIdentityForPID(pid, created, time.Now())
	})
}

func (s *Service) serveNativePlayerStats(w http.ResponseWriter, r *http.Request, identify func(*http.Request) (localPlayerIdentity, bool)) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "仅支持读取统计", http.StatusMethodNotAllowed)
		return
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	host, _, hostErr := net.SplitHostPort(r.Host)
	if err != nil || hostErr != nil || net.ParseIP(peer) == nil || !net.ParseIP(peer).IsLoopback() ||
		net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		http.Error(w, "玩家统计只允许本机游戏访问", http.StatusForbidden)
		return
	}
	for key := range r.URL.Query() {
		if key != "format" {
			http.Error(w, "统计请求参数无效", http.StatusBadRequest)
			return
		}
	}
	format := r.URL.Query().Get("format")
	if format != "" && format != "native-v1" && format != "native-v2" {
		http.Error(w, "统计格式无效", http.StatusBadRequest)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
		http.Error(w, "统计来源无效", http.StatusForbidden)
		return
	}
	identity, valid := identify(r)
	if !valid {
		http.Error(w, "请先用正常启动器登录并进入角色", http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	snapshot, err := s.buildPlayerActivitySnapshot(ctx, identity, s.gameplayNow())
	if err != nil {
		http.Error(w, "暂时无法读取账号统计，请稍后刷新", http.StatusServiceUnavailable)
		return
	}
	if after, ok := identify(r); !ok || after != identity {
		http.Error(w, "当前角色已变化，请重新打开统计", http.StatusConflict)
		return
	}
	if format == "native-v1" || format == "native-v2" {
		body, err := encodeNativePlayerActivityWithSources(snapshot, format == "native-v2")
		if err != nil {
			http.Error(w, "原生统计数据超出显示范围", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(snapshot)
}

// 自有本机HTTP适配格式，不是DNF游戏包：PSA1、UTF8短串、三段周期、固定五项int64。
func encodeNativePlayerActivity(snapshot playerActivitySnapshot) ([]byte, error) {
	return encodeNativePlayerActivityWithSources(snapshot, false)
}

// PSA1保持原字节；PSA2在各账号/角色的五项总量后增加四项史诗来源int64。
func encodeNativePlayerActivityWithSources(snapshot playerActivitySnapshot, sources bool) ([]byte, error) {
	return encodeNativePlayerActivityVersion(snapshot, sources, false)
}
func encodeNativePlayerActivityVersion(snapshot playerActivitySnapshot, sources, overview bool) ([]byte, error) {
	return encodeNativePlayerActivityDetails(snapshot, sources, overview, false)
}

// PSA4在PSA3的八项指标后附加五类金币；旧请求的字节布局保持。
func encodeNativePlayerActivityDetails(snapshot playerActivitySnapshot, sources, overview, gold bool) ([]byte, error) {
	if gold && !overview {
		return nil, errors.New("金币明细要求扩展快照")
	}
	var b bytes.Buffer
	if gold {
		b.WriteString("PSA4")
	} else if overview {
		b.WriteString("PSA3")
	} else if sources {
		b.WriteString("PSA2")
	} else {
		b.WriteString("PSA1")
	}
	writeString := func(value string) error {
		if len(value) > 4096 || strings.IndexByte(value, 0) >= 0 {
			return errors.New("原生统计文字超出限制")
		}
		_ = binary.Write(&b, binary.LittleEndian, uint16(len(value)))
		b.WriteString(value)
		return nil
	}
	texts := []string{snapshot.AccountID, snapshot.CurrentCharacter, snapshot.CurrentName, snapshot.UpdatedAt.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02 15:04:05")}
	if overview {
		if !sources || strings.TrimSpace(snapshot.AdventureName) == "" {
			return nil, errors.New("扩展统计缺少冒险团名称或来源")
		}
		texts = append(texts, snapshot.AdventureName)
	}
	metrics := playerActivityMetrics[:]
	if overview {
		metrics = append(metrics, repo.ActivityRecommendedDungeon, repo.ActivityColorless, repo.ActivityGoldSpent)
	}
	if gold {
		metrics = append(metrics, repo.ActivityRankSourceMetrics(repo.ActivityGoldSpent)...)
	}
	for _, value := range texts {
		if err := writeString(value); err != nil {
			return nil, err
		}
	}
	if len(snapshot.Periods) != 3 {
		return nil, errors.New("统计周期不完整")
	}
	for _, period := range snapshot.Periods {
		if len(period.Characters) > 1024 {
			return nil, errors.New("角色统计超出本机窗口容量")
		}
		for _, value := range []string{period.Key, period.Label, period.FromDay, period.ThroughDay} {
			if err := writeString(value); err != nil {
				return nil, err
			}
		}
		writeMetrics := func(values map[string]int64) error {
			if gold {
				var total int64
				for _, metric := range repo.ActivityRankSourceMetrics(repo.ActivityGoldSpent) {
					n := values[metric]
					if n < 0 || n > math.MaxInt64-total {
						return errors.New("金币明细统计值无效")
					}
					total += n
				}
				if total != values[repo.ActivityGoldSpent] {
					return errors.New("金币明细与养成合计不一致")
				}
			}
			for _, metric := range metrics {
				if values[metric] < 0 {
					return errors.New("统计值无效")
				}
				_ = binary.Write(&b, binary.LittleEndian, values[metric])
			}
			return nil
		}
		writeSources := func(values, metrics map[string]int64) error {
			if !sources {
				return nil
			}
			var total int64
			for _, key := range playerActivityEpicSourceKeys {
				n := values[key]
				if n < 0 || n > math.MaxInt64-total {
					return errors.New("史诗来源统计值无效")
				}
				total += n
				_ = binary.Write(&b, binary.LittleEndian, n)
			}
			if total != metrics[repo.ActivityEpic] {
				return errors.New("史诗来源分项与总数不一致")
			}
			return nil
		}
		if err := writeMetrics(period.Metrics); err != nil {
			return nil, err
		}
		if err := writeSources(period.EpicSources, period.Metrics); err != nil {
			return nil, err
		}
		_ = binary.Write(&b, binary.LittleEndian, uint16(len(period.Characters)))
		for _, role := range period.Characters {
			if err := writeString(role.ID); err != nil {
				return nil, err
			}
			if err := writeString(role.Name); err != nil {
				return nil, err
			}
			flag := byte(0)
			if role.Deleted {
				flag = 1
			}
			b.WriteByte(flag)
			if err := writeMetrics(role.Metrics); err != nil {
				return nil, err
			}
			if err := writeSources(role.EpicSources, role.Metrics); err != nil {
				return nil, err
			}
		}
	}
	if b.Len() > 1<<20 {
		return nil, errors.New("统计快照超出本机读取容量")
	}
	return b.Bytes(), nil
}
