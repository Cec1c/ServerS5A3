package quest

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestRealScriptPVFQuestDifficultyKeysAcrossArchiveDirectory 是"全目录"难度键审计。
//
// 修复把长度不为 1 的 `[difficulty]` 从静默退回 `G` 改成整 token 查表，所以必须证明
// `n_quest` **整个归档目录**（不只是 `quest.lst` 索引到的任务）里不存在"用了但权重表
// 没有"的**多字符**键，否则会把基线能结算（虽然少给经验）的任务变成无法结算。
//
// 真档实测（D29 pinned Script.pvf）：归档内 8920 个带 `[difficulty]` 的 .qst，使用的
// 规范化键与权重表同为 40 个，多字符键全部命中；未命中的只有单字符 `R`(19) 与
// `0`(1)。基线只在单 rune 时投影难度，所以 R/0 在基线同样落到 `ErrQuestDifficulty`：
// 行为不变，不属于本项范围（不修，只记录）。
func TestRealScriptPVFQuestDifficultyKeysAcrossArchiveDirectory(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to audit two-digit quest difficulty keys across the archive")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	tables, err := progression.Load(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	used := make(map[string]int)
	multiCharacter := make(map[string]int)
	scanned := 0
	err = archive.ScanTextPrefix("n_quest", func(archivePath, text string) error {
		if !strings.HasSuffix(strings.ToLower(archivePath), ".qst") {
			return nil
		}
		doc, parseErr := dnfpvf.Parse(archivePath, text)
		if parseErr != nil {
			return nil
		}
		raw, ok := doc.Text("difficulty")
		if !ok {
			return nil
		}
		key := strings.ToUpper(strings.TrimSpace(raw))
		if key == "" {
			return nil
		}
		used[key]++
		if len([]rune(key)) > 1 {
			multiCharacter[key]++
		}
		scanned++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 8000 {
		t.Fatalf("archive scan only saw %d quests with [difficulty]; the directory walk looks broken", scanned)
	}
	if len(multiCharacter) == 0 {
		t.Fatal("no multi-character difficulty quest found in the archive directory")
	}
	unknown := make(map[string]int)
	for key, count := range used {
		if _, err := tables.QuestExperience(1, 1, key, "", false); errors.Is(err, progression.ErrQuestDifficulty) {
			unknown[key] = count
		}
	}
	for key, count := range unknown {
		if len([]rune(key)) > 1 {
			t.Errorf("archive difficulty %q used by %d quests is multi-character but absent from the weight table: baseline settled it with G, this candidate would fail closed", key, count)
		}
		if key != "R" && key != "0" {
			t.Errorf("unexpected unknown single-character difficulty %q used by %d quests", key, count)
		}
	}
	t.Logf("archive difficulty audit: scanned=%d distinct=%d multi-character=%d unknown=%v",
		scanned, len(used), len(multiCharacter), unknown)
}
