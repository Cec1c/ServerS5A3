package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// 同一文字夹具供客户端C++解码器读取，防止两端字段顺序各自正确却不相容。
func TestNativePlayerEpicSourceSharedCodecFixture(t *testing.T) {
	metrics := map[string]int64{repo.ActivityFatigue: 156, repo.ActivityAbyss: 3, repo.ActivityEpic: 10, repo.ActivityUnique: 2, repo.ActivityLegendary: 1}
	sources := map[string]int64{"epic_road": 4, "demon_rift": 3, "time_rift": 2, "other_dungeons": 1}
	s := playerActivitySnapshot{AccountID: "a", CurrentCharacter: "2", CurrentName: "测试角色", UpdatedAt: time.Date(2026, 10, 29, 4, 34, 56, 0, time.UTC)}
	for _, p := range []playerActivityPeriod{{Key: "today", Label: "今日", FromDay: "2026-10-29", ThroughDay: "2026-10-29"}, {Key: "week", Label: "本周", FromDay: "2026-10-26", ThroughDay: "2026-10-29"}, {Key: "all", Label: "累计"}} {
		p.Metrics, p.EpicSources = metrics, sources
		p.Characters = []playerActivityRole{{ID: "2", Name: "测试角色", Metrics: metrics, EpicSources: sources}, {ID: "7", Name: "历史角色", Deleted: true, Metrics: map[string]int64{}, EpicSources: map[string]int64{}}}
		s.Periods = append(s.Periods, p)
	}
	encoded, err := encodeNativePlayerActivityWithSources(s, true)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/player_activity_psa2.hex")
	if err != nil {
		t.Fatal(err)
	}
	want, err := hex.DecodeString(strings.TrimSpace(string(fixture)))
	if err != nil || !bytes.Equal(encoded, want) {
		t.Fatalf("Go/客户端共用PSA2夹具不匹配：%v", err)
	}
}

func TestNativePlayerEpicSourceSnapshotPeriodsRolesAndFormatCompatibility(t *testing.T) {
	s, identity, now := playerStatsFixture(t)
	g, _ := s.repositoryGroup()
	for i, row := range []struct {
		character string
		dungeon   int64
	}{{"2", 6062}, {"2", 6063}, {"2", 6064}, {"2", 6065}, {"10", 316}, {"7", 7539}, {"2", 310}, {"99", 316}} {
		account := "a"
		if row.character == "99" {
			account = "b"
		}
		e := repo.ActivityEvent{ID: fmt.Sprintf("source-%d", i), AccountID: account, CharacterID: row.character, DungeonID: row.dungeon, Metric: repo.ActivityEpic, Amount: 1, ItemID: 9001, Rarity: 4, Source: "dungeon_pickup", OccurredAt: now}
		if err := g.CharacterSettlement.WithinCharacterSettlement(context.Background(), row.character, func(tx repo.Group) error { _, err := tx.Activity.Record(context.Background(), e); return err }); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := s.buildPlayerActivitySnapshot(context.Background(), identity, now)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range snapshot.Periods {
		want := map[string]int64{"epic_road": 4, "demon_rift": 1, "time_rift": 1, "other_dungeons": 1}
		if i == 2 {
			want["other_dungeons"] = 2
		}
		if !reflect.DeepEqual(p.EpicSources, want) {
			t.Fatalf("周期%s来源错误：%v/%v", p.Key, p.EpicSources, want)
		}
		var total int64
		for _, n := range p.EpicSources {
			total += n
		}
		if total != p.Metrics[repo.ActivityEpic] {
			t.Fatal("总数与分项不符", p)
		}
		for _, role := range p.Characters {
			if role.ID == "99" {
				t.Fatal("混入其他账号来源")
			}
			var n int64
			for _, v := range role.EpicSources {
				n += v
			}
			if n != role.Metrics[repo.ActivityEpic] {
				t.Fatal("角色分项错误", role)
			}
		}
	}
	for _, role := range snapshot.Periods[2].Characters {
		if role.ID == "7" && (!role.Deleted || role.EpicSources["time_rift"] != 1 || role.EpicSources["other_dungeons"] != 1) {
			t.Fatal("删除角色来源丢失", role)
		}
	}
	rolled, err := s.buildPlayerActivitySnapshot(context.Background(), identity, now.Add(time.Minute))
	if err != nil || rolled.Periods[0].Metrics[repo.ActivityEpic] != 0 || rolled.Periods[1].Metrics[repo.ActivityEpic] != 0 || rolled.Periods[2].Metrics[repo.ActivityEpic] != 8 {
		t.Fatal("周一06点周期错误", rolled, err)
	}
	legacy, err := encodeNativePlayerActivity(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	without := snapshot
	without.Periods = append([]playerActivityPeriod{}, snapshot.Periods...)
	for i := range without.Periods {
		without.Periods[i].EpicSources = nil
		without.Periods[i].Characters = append([]playerActivityRole{}, snapshot.Periods[i].Characters...)
		for j := range without.Periods[i].Characters {
			without.Periods[i].Characters[j].EpicSources = nil
		}
	}
	old, err := encodeNativePlayerActivity(without)
	if err != nil || !bytes.Equal(legacy, old) {
		t.Fatal("PSA1旧字节契约改变", err)
	}
	v2, err := encodeNativePlayerActivityWithSources(snapshot, true)
	if err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(v2)
	magic := make([]byte, 4)
	reader.Read(magic)
	if string(magic) != "PSA2" {
		t.Fatal("格式标记错误")
	}
	readString := func() string {
		t.Helper()
		var n uint16
		if err := binary.Read(reader, binary.LittleEndian, &n); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, n)
		if _, err := reader.Read(b); err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for i := 0; i < 4; i++ {
		readString()
	}
	readValues := func(metrics, sources map[string]int64) {
		t.Helper()
		for _, key := range playerActivityMetrics {
			var n int64
			if err := binary.Read(reader, binary.LittleEndian, &n); err != nil || n != metrics[key] {
				t.Fatal("总量编码错误", key, n, err)
			}
		}
		for _, key := range playerActivityEpicSourceKeys {
			var n int64
			if err := binary.Read(reader, binary.LittleEndian, &n); err != nil || n != sources[key] {
				t.Fatal("来源编码错误", key, n, err)
			}
		}
	}
	for _, p := range snapshot.Periods {
		for i := 0; i < 4; i++ {
			readString()
		}
		readValues(p.Metrics, p.EpicSources)
		var count uint16
		if err := binary.Read(reader, binary.LittleEndian, &count); err != nil || int(count) != len(p.Characters) {
			t.Fatal("角色数错误", count, err)
		}
		for _, role := range p.Characters {
			if readString() != role.ID || readString() != role.Name {
				t.Fatal("角色身份错位")
			}
			if _, err := reader.ReadByte(); err != nil {
				t.Fatal(err)
			}
			readValues(role.Metrics, role.EpicSources)
		}
	}
	if reader.Len() != 0 {
		t.Fatal("PSA2存在未消费字节", reader.Len())
	}
	request := httptest.NewRequest("GET", "http://127.0.0.1:18111/local/player-stats?format=native-v2", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	s.serveNativePlayerStats(w, request, func(*http.Request) (localPlayerIdentity, bool) { return identity, true })
	if w.Code != 200 || !bytes.HasPrefix(w.Body.Bytes(), []byte("PSA2")) {
		t.Fatal("新格式入口失败", w.Code, w.Body.String())
	}
	for _, bad := range []map[string]int64{{"epic_road": -1}, {"epic_road": math.MaxInt64, "demon_rift": 1}, {"epic_road": 0}} {
		snapshot.Periods[0].EpicSources = bad
		if _, err := encodeNativePlayerActivityWithSources(snapshot, true); err == nil {
			t.Fatal("接受错误来源统计", bad)
		}
	}
}
