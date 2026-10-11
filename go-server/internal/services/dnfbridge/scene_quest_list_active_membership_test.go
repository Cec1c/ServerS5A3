package dnfbridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 2026-10-02 (V20)：这次"限定任务不显示"的排查里，`quest_ids` 这个**有界**日志（只记前 32 条）
// 被当成了全量：10019/2901 排在第 32 条之后，于是"不在 quest_ids 里"被读成"服务端没下发"，
// 直到直接解 op21 包体（protobuf field4 的 packed varint 列表）才推翻。
//
// 这个用例钉住修正后的口径：
//   - 有界字段必须自带 `quest_ids_is_bounded_log` 与 `quest_ids_total`，不能让人再误读；
//   - `active_ids_in_list` / `active_ids_missing` 是"进行中任务 vs 可接受列表"的只读比对，
//     前者给客户端"进行中"页依赖的集合，后者是**真正的服务端缺陷信号**（正常为空）。
func TestAcceptableQuestListLogsActiveMembership(t *testing.T) {
	source := questListTestSource{
		dnfquest.DefaultList: "100 `main.qst`\n102 `active.qst`\n103 `hidden.qst`\n",
		"n_quest/main.qst":   "[grade]\n`[epic]`\n[level]\n1 10\n[job]\n`[gunner]`\n[exposed by npc]\n1\n",
		"n_quest/active.qst": "[grade]\n`[normal]`\n[level]\n1 10\n[job]\n`[gunner]`\n[exposed by npc]\n1\n",
		// exposed by npc = 0 ⇒ definitionAcceptable 直接排除，用来制造一个"进行中却不在列表里"的任务。
		"n_quest/hidden.qst": "[grade]\n`[normal]`\n[level]\n1 10\n[job]\n`[gunner]`\n[exposed by npc]\n0\n",
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "12",
		AccountID:   defaultAccountPrefix + "1",
		Name:        "gunner",
		Job:         "2",
		Level:       2,
		Stats:       map[string]int64{"grow_type": 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(context.Background(), dnfrepo.QuestRecord{
		CharacterID: "12",
		States: map[int64]dnfrepo.QuestState{
			102: {Status: "active", ProgressValue: 1},
			103: {Status: "active", ProgressValue: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{
		questCatalog:       catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		packetLog:          logger,
	}
	session := &gameSession{conn: &bufferConn{}, connID: "quest-membership", selectedCharacterID: 12}
	if _, ok := service.buildCurrentAcceptableQuestListBodyForSession(context.Background(), session); !ok {
		t.Fatal("acceptable quest list was skipped")
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "kind=game-upper-current-acceptable-quest-list-built") {
		t.Fatalf("quest list event missing:\n%s", text)
	}
	for _, want := range []string{
		"active_ids_in_list=102",
		"active_ids_missing=103",
		"quest_ids_is_bounded_log=false",
		"quest_ids_total=2",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("log lacks %q:\n%s", want, text)
		}
	}
}

// 同一族的第二个用例：全部进行中任务都在列表里时，`active_ids_missing` 必须是显式的 "-"
// （空字段会被读成"没这条字段"，所以空集合要自己写出来）。
func TestAcceptableQuestListActiveMissingRendersExplicitDash(t *testing.T) {
	source := questListTestSource{
		dnfquest.DefaultList: "100 `main.qst`\n102 `active.qst`\n",
		"n_quest/main.qst":   "[grade]\n`[epic]`\n[level]\n1 10\n[job]\n`[gunner]`\n[exposed by npc]\n1\n",
		"n_quest/active.qst": "[grade]\n`[normal]`\n[level]\n1 10\n[job]\n`[gunner]`\n[exposed by npc]\n1\n",
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "12", AccountID: defaultAccountPrefix + "1", Name: "gunner",
		Job: "2", Level: 2, Stats: map[string]int64{"grow_type": 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(context.Background(), dnfrepo.QuestRecord{
		CharacterID: "12",
		States:      map[int64]dnfrepo.QuestState{102: {Status: "active", ProgressValue: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{
		questCatalog:       catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		packetLog:          logger,
	}
	if _, ok := service.buildCurrentAcceptableQuestListBodyForSession(context.Background(),
		&gameSession{conn: &bufferConn{}, connID: "quest-membership-2", selectedCharacterID: 12}); !ok {
		t.Fatal("acceptable quest list was skipped")
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(raw); !strings.Contains(text, "active_ids_missing=-") ||
		!strings.Contains(text, "active_ids_in_list=102") {
		t.Fatalf("membership fields wrong:\n%s", text)
	}
}
