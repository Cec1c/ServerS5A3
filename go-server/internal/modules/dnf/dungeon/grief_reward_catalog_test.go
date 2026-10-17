package dungeon

import (
	"errors"
	"os"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	griefRewardTowerPath = "etc/towerofgrief.etc"
	griefRewardDropPath  = "etc/itemdropinfo_togclearreward.etc"
	griefRewardTestKey   = "round31-grief-reference"

	griefRewardTowerText = "[each layer dungeon clear reward]\n" +
		"1 49 `tower_grief_reward_normal` 50 50 `tower_grief_reward_special` " +
		"51 99 `tower_grief_reward_normal` 100 100 `tower_grief_reward_special`\n" +
		"[/each layer dungeon clear reward]\n"

	griefRewardDropText = "[table]\n" +
		"`tower_grief_reward_normal` " +
		"`card 3` 10160931 2850 1 `card 4` 10160931 2850 1 `card 5` 10160931 2850 1 " +
		"`card 6` 10160931 2850 1 `card 7` 10160931 2850 1 `card 8` 10160931 2850 1 " +
		"`card 9` 10160931 2850 1 `card 10` 10160931 2800 1 `card 10` 10303266 500 1\n" +
		"[/table]\n" +
		"[table]\n" +
		"`tower_grief_reward_special` " +
		"`card 3` 10160931 2850 1 `card 4` 10160931 2850 1 `card 5` 10160931 2850 1 " +
		"`card 6` 10160931 2850 1 `card 7` 10160931 2850 1 `card 8` 10160931 2850 1 " +
		"`card 9` 10160931 2850 1 `card 9` 10303266 500 1 `card 10` 10160930 10000 1\n" +
		"[/table]\n"
)

var errGriefRewardTestRead = errors.New("grief reward test source read failure")

type griefRewardTestSource struct {
	documents  map[string]string
	readErrors map[string]error
	reads      []string
}

func newGriefRewardTestSource() *griefRewardTestSource {
	return &griefRewardTestSource{
		documents: map[string]string{
			griefRewardTowerPath: griefRewardTowerText,
			griefRewardDropPath:  griefRewardDropText,
		},
		readErrors: make(map[string]error),
	}
}

func (source *griefRewardTestSource) ReadText(path string) (string, error) {
	source.reads = append(source.reads, path)
	if err := source.readErrors[path]; err != nil {
		return "", err
	}
	text, ok := source.documents[path]
	if !ok {
		return "", os.ErrNotExist
	}
	return text, nil
}

func TestGriefRewardCandidatesMatchesReferenceSummary(t *testing.T) {
	source := newGriefRewardTestSource()
	got, err := GriefRewardCandidates(source, 1, griefRewardTestKey)
	if err != nil {
		t.Fatal(err)
	}
	want := griefRewardNormalReference()
	if got != want {
		t.Fatalf("fixed-key reference candidates=%+v want=%+v", got, want)
	}
	if strings.Join(source.reads, "|") != griefRewardTowerPath+"|"+griefRewardDropPath {
		t.Fatalf("read paths=%v", source.reads)
	}
}

func TestGriefRewardCandidatesKeepsTenIndependentPositionsAndRepeats(t *testing.T) {
	source := newGriefRewardTestSource()
	for _, floor := range []uint16{50, 100} {
		first, err := GriefRewardCandidates(source, floor, griefRewardTestKey)
		if err != nil {
			t.Fatalf("floor=%d err=%v", floor, err)
		}
		second, err := GriefRewardCandidates(source, floor, griefRewardTestKey)
		if err != nil {
			t.Fatalf("floor=%d repeat err=%v", floor, err)
		}
		if first != second {
			t.Fatalf("floor=%d fixed key changed: first=%+v second=%+v", floor, first, second)
		}
		if want := griefRewardSpecialReference(); first != want {
			t.Fatalf("floor=%d candidate positions changed: got=%+v want=%+v", floor, first, want)
		}
	}
}

func TestGriefRewardCandidatesRejectsBadCatalogs(t *testing.T) {
	if got, err := GriefRewardCandidates(nil, 1, griefRewardTestKey); err == nil || got != ([GriefRewardSlots]GriefReward{}) {
		t.Fatalf("nil source candidates=%+v err=%v", got, err)
	}
	for _, tc := range []struct {
		name  string
		floor uint16
		key   string
	}{
		{name: "floor zero", floor: 0, key: griefRewardTestKey},
		{name: "floor past end", floor: 101, key: griefRewardTestKey},
		{name: "empty key", floor: 1, key: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := GriefRewardCandidates(newGriefRewardTestSource(), tc.floor, tc.key); !errors.Is(err, ErrSettlementStateInvalid) || got != ([GriefRewardSlots]GriefReward{}) {
				t.Fatalf("candidates=%+v err=%v", got, err)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *griefRewardTestSource)
	}{
		{"tower source failure", func(_ *testing.T, source *griefRewardTestSource) {
			source.readErrors[griefRewardTowerPath] = errGriefRewardTestRead
		}},
		{"drop source failure", func(_ *testing.T, source *griefRewardTestSource) {
			source.readErrors[griefRewardDropPath] = errGriefRewardTestRead
		}},
		{"unterminated tower source", func(_ *testing.T, source *griefRewardTestSource) {
			source.documents[griefRewardTowerPath] += "`unterminated"
		}},
		{"missing reward section", func(_ *testing.T, source *griefRewardTestSource) {
			source.documents[griefRewardTowerPath] = "[other]\n1 49 `tower_grief_reward_normal`\n"
		}},
		{"malformed reward row width", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardTowerPath, "1 49 `", "1 49 7 `")
		}},
		{"non-string reward table name", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardTowerPath, "`tower_grief_reward_normal`", "tower_grief_reward_normal")
		}},
		{"empty reward table name", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardTowerPath, "`tower_grief_reward_normal`", "``")
		}},
		{"overlapping reward ranges", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardTowerPath, "50 50", "49 50")
		}},
		{"missing floor", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardTowerPath, "1 49", "2 49")
		}},
		{"zero range start", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardTowerPath, "1 49", "0 49")
		}},
		{"range past end", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardTowerPath, "100 100", "100 101")
		}},
		{"reversed range", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardTowerPath, "50 50", "50 49")
		}},
		{"missing drop table", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardDropPath, "`tower_grief_reward_normal`", "`other_normal`")
		}},
		{"non-string table name", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardDropPath, "`tower_grief_reward_normal`", "tower_grief_reward_normal")
		}},
		{"empty table section", func(_ *testing.T, source *griefRewardTestSource) {
			source.documents[griefRewardDropPath] = "[table]\n[/table]\n" + source.documents[griefRewardDropPath]
		}},
		{"duplicate matching table", func(_ *testing.T, source *griefRewardTestSource) {
			source.documents[griefRewardDropPath] += "[table]\n`tower_grief_reward_normal` `card 1` 1 1 1\n[/table]\n"
		}},
		{"malformed table row width", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardDropPath, "`card 10` 10303266 500 1\n", "`card 10` 10303266 500 1 7\n")
		}},
		{"invalid card label", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardDropPath, "`card 3`", "`slot 3`")
		}},
		{"card zero", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardDropPath, "`card 3`", "`card 0`")
		}},
		{"card past end", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardDropPath, "`card 3`", "`card 11`")
		}},
		{"zero item", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardDropPath, "`card 3` 10160931 2850 1", "`card 3` 0 2850 1")
		}},
		{"zero count", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardDropPath, "`card 3` 10160931 2850 1", "`card 3` 10160931 2850 0")
		}},
		{"negative probability", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardDropPath, "`card 3` 10160931 2850 1", "`card 3` 10160931 -1 1")
		}},
		{"probability past end", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardDropPath, "`card 3` 10160931 2850 1", "`card 3` 10160931 10001 1")
		}},
		{"non-integer probability", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardDropPath, "`card 3` 10160931 2850 1", "`card 3` 10160931 2.5 1")
		}},
		{"cumulative probability past 10000", func(t *testing.T, source *griefRewardTestSource) {
			mustReplaceGriefRewardText(t, source, griefRewardDropPath, "`card 10` 10160931 2800 1", "`card 10` 10160931 10000 1")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := newGriefRewardTestSource()
			tc.mutate(t, source)
			got, err := GriefRewardCandidates(source, 1, griefRewardTestKey)
			if err == nil {
				t.Fatalf("bad catalog accepted: %+v", got)
			}
			if got != ([GriefRewardSlots]GriefReward{}) {
				t.Fatalf("error returned partial candidates: %+v", got)
			}
		})
	}
}

func TestGriefRewardCandidatesRealPVF(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the paired Script.pvf")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for floor := uint16(1); floor <= 100; floor++ {
		first, err := GriefRewardCandidates(archive, floor, griefRewardTestKey)
		if err != nil {
			t.Fatalf("floor=%d err=%v", floor, err)
		}
		second, err := GriefRewardCandidates(archive, floor, griefRewardTestKey)
		if err != nil || first != second {
			t.Fatalf("floor=%d fixed key is unstable: first=%+v second=%+v err=%v", floor, first, second, err)
		}
		want := griefRewardNormalReference()
		if floor == 50 || floor == 100 {
			want = griefRewardSpecialReference()
		}
		if first != want {
			t.Fatalf("floor=%d candidates=%+v want=%+v", floor, first, want)
		}
	}
}

func griefRewardNormalReference() [GriefRewardSlots]GriefReward {
	// Python hashlib reference rolls for griefRewardTestKey:
	// 5795 759 2496 4577 4297 818 4969 6286 8885 7391.
	return [GriefRewardSlots]GriefReward{
		{},
		{},
		{ItemID: 10160931, Count: 1},
		{},
		{},
		{ItemID: 10160931, Count: 1},
		{},
		{},
		{},
		{},
	}
}

func griefRewardSpecialReference() [GriefRewardSlots]GriefReward {
	return [GriefRewardSlots]GriefReward{
		{},
		{},
		{ItemID: 10160931, Count: 1},
		{},
		{},
		{ItemID: 10160931, Count: 1},
		{},
		{},
		{},
		{ItemID: 10160930, Count: 1},
	}
}

func mustReplaceGriefRewardText(t *testing.T, source *griefRewardTestSource, path, old, replacement string) {
	t.Helper()
	text := source.documents[path]
	if !strings.Contains(text, old) {
		t.Fatalf("%s fixture does not contain %q", path, old)
	}
	if old == replacement {
		t.Fatalf("%s fixture replacement is a no-op: %q", path, old)
	}
	replaced := strings.Replace(text, old, replacement, 1)
	if replaced == text {
		t.Fatalf("%s fixture replacement did not change the document: %q", path, old)
	}
	source.documents[path] = replaced
}
