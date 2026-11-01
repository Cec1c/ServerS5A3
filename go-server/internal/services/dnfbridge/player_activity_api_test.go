package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestNativePlayerIdentityRejectsUnprovenExpiredReusedAndAmbiguous(t *testing.T) {
	now := time.Now()
	born := now.Add(-time.Hour)
	entry := clientAccountRegistration{accountID: "a", tokenAccountID: "a", tokenConfirmedAt: born.Add(time.Minute), expiresAt: now.Add(time.Hour)}
	for _, name := range []string{"正常", "只有登记", "过期", "PID复用", "账号不符", "角色退出", "并行角色", "会话取消"} {
		t.Run(name, func(t *testing.T) {
			s := &Service{clientAccounts: newClientAccountRegistry(), gameSessions: map[uint16]*gameSession{2: {clientPID: 123, accountID: "a", accountFromToken: true, selectedCharacterID: 2, characterGeneration: 9}}}
			e := entry
			creation := born
			switch name {
			case "只有登记":
				e.tokenAccountID = ""
			case "过期":
				e.expiresAt = now
			case "PID复用":
				creation = now
			case "账号不符":
				s.gameSessions[2].accountID = "b"
			case "角色退出":
				s.gameSessions[2].selectedCharacterID = 0
			case "并行角色":
				s.gameSessions[3] = &gameSession{clientPID: 123, accountID: "a", accountFromToken: true, selectedCharacterID: 3}
			case "会话取消":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				s.gameSessions[2].ctx = ctx
			}
			s.clientAccounts.byPID[123] = e
			got, ok := s.localPlayerIdentityForPID(123, creation, now)
			if ok != (name == "正常") {
				t.Fatalf("身份判定=%v %+v", ok, got)
			}
			if ok && (got.AccountID != "a" || got.CharacterID != "2" || got.Generation != 9) {
				t.Fatal(got)
			}
		})
	}
}

func playerStatsFixture(t *testing.T) (*Service, localPlayerIdentity, time.Time) {
	t.Helper()
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	now := time.Date(2026, 10, 26, 5, 59, 0, 0, time.FixedZone("CN", 8*3600))
	for _, ch := range []repo.CharacterRecord{{AccountID: "a", CharacterID: "2", Name: "当前角色"}, {AccountID: "a", CharacterID: "10", Name: "未刷图角色"}, {AccountID: "a", CharacterID: "7", Name: "即将删除角色"}, {AccountID: "b", CharacterID: "99", Name: "其它账号"}} {
		if err := g.Character.Save(ctx, ch); err != nil {
			t.Fatal(err)
		}
	}
	for _, ev := range []repo.ActivityEvent{
		{ID: "today", AccountID: "a", CharacterID: "2", Metric: repo.ActivityFatigue, Amount: 1, OccurredAt: now, Source: "dungeon_room", DungeonID: 310},
		{ID: "week", AccountID: "a", CharacterID: "2", Metric: repo.ActivityAbyss, Amount: 1, OccurredAt: now.AddDate(0, 0, -2), Source: "hell_party", DungeonID: 310},
		{ID: "old", AccountID: "a", CharacterID: "7", Metric: repo.ActivityEpic, Amount: 1, OccurredAt: now.AddDate(0, 0, -20), Source: "dungeon_pickup", DungeonID: 310, ItemID: 123, Rarity: 4},
		{ID: "other", AccountID: "b", CharacterID: "99", Metric: repo.ActivityFatigue, Amount: 1, OccurredAt: now, Source: "dungeon_room", DungeonID: 310},
	} {
		if err := g.CharacterSettlement.WithinCharacterSettlement(ctx, ev.CharacterID, func(tx repo.Group) error { _, err := tx.Activity.Record(ctx, ev); return err }); err != nil {
			t.Fatal(err)
		}
	}
	removed, _, err := g.Character.Load(ctx, "7")
	if err != nil {
		t.Fatal(err)
	}
	removed.Stats = map[string]int64{"delete_flag": 1}
	if err := g.Character.Save(ctx, removed); err != nil {
		t.Fatal(err)
	}
	return &Service{repositoryProvider: func() (repo.Group, bool) { return g, true }}, localPlayerIdentity{AccountID: "a", CharacterID: "2", Generation: 1}, now
}

func TestNativePlayerSnapshotServiceDayAccountAndHistoricalRoles(t *testing.T) {
	s, id, now := playerStatsFixture(t)
	got, err := s.buildPlayerActivitySnapshot(context.Background(), id, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Periods[0].FromDay != "2026-10-25" || got.Periods[1].FromDay != "2026-10-19" || got.Periods[1].ThroughDay != "2026-10-25" {
		t.Fatal(got.Periods)
	}
	for i, want := range [][3]int64{{1, 0, 0}, {1, 1, 0}, {1, 1, 1}} {
		p := got.Periods[i]
		if p.Metrics[repo.ActivityFatigue] != want[0] || p.Metrics[repo.ActivityAbyss] != want[1] || p.Metrics[repo.ActivityEpic] != want[2] {
			t.Fatal(p)
		}
		if len(p.Metrics) != 5 || p.Characters[0].ID != "2" || p.Characters[1].ID != "10" {
			t.Fatal(p)
		}
		for _, ch := range p.Characters {
			if ch.ID == "99" {
				t.Fatal("混入其它账号")
			}
		}
	}
	old := got.Periods[2].Characters[2]
	if old.ID != "7" || !old.Deleted || old.Metrics[repo.ActivityEpic] != 1 {
		t.Fatal(old)
	}
	from, _ := playerActivityDayRange(now.Add(time.Minute), "week")
	if from != "2026-10-26" {
		t.Fatal(from)
	}
	id.AccountID = "b"
	if _, err := s.buildPlayerActivitySnapshot(context.Background(), id, now); err == nil {
		t.Fatal("允许跨账号角色")
	}
}

func TestNativePlayerHTTPRejectsUntrustedAndChangedIdentity(t *testing.T) {
	s, id, _ := playerStatsFixture(t)
	for _, tc := range []struct {
		name, method, url, peer, host, origin string
		status                                int
		unproven, changed                     bool
	}{
		{name: "正常", status: 200}, {name: "原生", url: "http://127.0.0.1:18111/local/player-stats?format=native-v1", status: 200},
		{name: "写入", method: "POST", status: 405}, {name: "远端", peer: "192.168.1.2:1", status: 403},
		{name: "DNS域名", host: "evil.test:18111", status: 403}, {name: "外站", origin: "https://evil.test", status: 403},
		{name: "选账号", url: "http://127.0.0.1:18111/local/player-stats?account_id=b", status: 400},
		{name: "未登录", unproven: true, status: 401}, {name: "查询中切号", changed: true, status: 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method := tc.method
			if method == "" {
				method = "GET"
			}
			url := tc.url
			if url == "" {
				url = "http://127.0.0.1:18111/local/player-stats"
			}
			r := httptest.NewRequest(method, url, nil)
			r.RemoteAddr = "127.0.0.1:12345"
			if tc.peer != "" {
				r.RemoteAddr = tc.peer
			}
			if tc.host != "" {
				r.Host = tc.host
			}
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			calls := 0
			s.serveNativePlayerStats(w, r, func(*http.Request) (localPlayerIdentity, bool) {
				calls++
				out := id
				if tc.changed && calls > 1 {
					out.Generation++
				}
				return out, !tc.unproven
			})
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(w.Header())
			}
			if w.Code == 200 {
				if tc.name == "原生" {
					if !bytes.HasPrefix(w.Body.Bytes(), []byte("PSA1")) {
						t.Fatal("适配头缺失")
					}
				} else {
					var snapshot playerActivitySnapshot
					if json.Unmarshal(w.Body.Bytes(), &snapshot) != nil || snapshot.AccountID != "a" {
						t.Fatal(w.Body.String())
					}
				}
			} else if bytes.Contains(w.Body.Bytes(), []byte("其它账号")) || bytes.Contains(w.Body.Bytes(), []byte("current_character")) {
				t.Fatal("失败响应泄漏数据")
			}
		})
	}
}

func TestNativePlayerBinaryCarriesUTF8AndInt64WithoutTruncation(t *testing.T) {
	s, id, now := playerStatsFixture(t)
	snapshot, err := s.buildPlayerActivitySnapshot(context.Background(), id, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Periods[0].Metrics[repo.ActivityFatigue] = 1 << 40
	body, err := encodeNativePlayerActivity(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(body[4:])
	readString := func() string {
		var n uint16
		if binary.Read(r, binary.LittleEndian, &n) != nil {
			t.Fatal("短串头")
		}
		b := make([]byte, n)
		if _, err := r.Read(b); err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if readString() != "a" || readString() != "2" || readString() != "当前角色" {
		t.Fatal("身份或UTF8损坏")
	}
	readString()
	if readString() != "today" || readString() != "今日" {
		t.Fatal("周期损坏")
	}
	readString()
	readString()
	var value int64
	if binary.Read(r, binary.LittleEndian, &value) != nil || value != 1<<40 {
		t.Fatal(value)
	}
	snapshot.CurrentName = "bad\x00name"
	if _, err := encodeNativePlayerActivity(snapshot); err == nil {
		t.Fatal("允许NUL文字")
	}
	snapshot.CurrentName = "正常"
	snapshot.Periods[0].Metrics[repo.ActivityFatigue] = -1
	if _, err := encodeNativePlayerActivity(snapshot); err == nil {
		t.Fatal("允许负统计")
	}
}
