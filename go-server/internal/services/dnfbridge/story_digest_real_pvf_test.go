package dnfbridge

import (
	"os"
	"strconv"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本文件用运行时真实 Script.pvf 给"账号级记录覆盖时下发角色当前等级"这条口径
// 提供证据：
//
//   - etc/storydigest/digest.etc 把剧情回顾按等级分成 17 段（[level] 17 23、
//     24 29 … 90 999），每段有自己的 [ending] 场景；
//   - etc/storydigest/scene.etc 里这些 [ending] 场景就是客户端播的东西
//     （大多指向 Video/movie/*.avi 影片，例如第 17 段 → 917 → cinematic_11.avi）；
//   - 客户端 native story-digest manager（对象 +0x44 存 last_level，见
//     2026-09-25 trace）拿 op1378 的等级与角色当前等级各自选段比较：
//     段不同 ⇒ 每次登录都强制播放（操作者现象：等级 90、记录停在 40）；
//     段相同 ⇒ 不播。
//
// 因此"账号级 viewed_level >= 角色当前等级 ⇒ 下发角色当前等级"能保证
// 段相同，等于客户端认为全部看过。
//
// 运行：DNFBRIDGE_REAL_PVF_SMOKE=<runtime>\data\dnf\Script.pvf go test -run RealStoryDigest ./internal/services/dnfbridge

const (
	realStoryDigestDocPath   = "etc/storydigest/digest.etc"
	realStoryDigestScenePath = "etc/storydigest/scene.etc"
)

type realStoryDigestSegment struct {
	id     int64
	min    int64
	max    int64
	ending int64
}

func TestRealStoryDigestLevelSegmentsCoverEveryForcedPlaybackLevel(t *testing.T) {
	segments, scenes := loadRealStoryDigestTables(t)
	if len(segments) != 17 {
		t.Fatalf("real story digest segments = %d, want 17: %+v", len(segments), segments)
	}
	// 段区间必须连续覆盖 17..999：每个等级恰好落在一段里，没有空洞 ⇒
	// "段相同"就是"不会播"的充分条件，而不是"两段都找不到"的巧合。
	want := int64(17)
	for _, segment := range segments {
		if segment.min != want {
			t.Fatalf("real segment %d level range [%d,%d] does not continue at %d", segment.id, segment.min, segment.max, want)
		}
		if segment.max < segment.min {
			t.Fatalf("real segment %d has an empty range [%d,%d]", segment.id, segment.min, segment.max)
		}
		want = segment.max + 1
	}
	if want != 1000 {
		t.Fatalf("real story digest level coverage ends at %d, want 1000 (levels up to 999)", want)
	}

	// 每段的 [ending] 场景在 scene.etc 里真实存在；等级 90 那段（第 17 段）
	// 指向的就是操作者看到的影片。
	for _, segment := range segments {
		values, ok := scenes[segment.ending]
		if !ok || len(values) == 0 {
			t.Fatalf("real segment %d ending scene %d is undefined in %s", segment.id, segment.ending, realStoryDigestScenePath)
		}
	}
	levelEighty := segments[len(segments)-1]
	if levelEighty.min != 90 || levelEighty.max != 999 {
		t.Fatalf("last real segment range = [%d,%d], want [90,999]", levelEighty.min, levelEighty.max)
	}
	foundVideo := false
	for _, value := range scenes[levelEighty.ending] {
		if strings.Contains(strings.ToLower(value), "video/movie/") && strings.HasSuffix(strings.ToLower(value), ".avi") {
			foundVideo = true
			break
		}
	}
	if !foundVideo {
		t.Fatalf("real segment %d ending %d is not a video cinematic: %v", levelEighty.id, levelEighty.ending, scenes[levelEighty.ending])
	}
}

func TestRealStoryDigestAccountRecordPreventsForcedPlayback(t *testing.T) {
	segments, _ := loadRealStoryDigestTables(t)
	segmentOf := func(level int64) int64 {
		return realStoryDigestSegmentIDForLevel(segments, level)
	}
	// 操作者现象（char3：等级被 GM 拔到 90，角色自己的记录停在 40）：段 5 vs
	// 段 17 ⇒ 客户端认为落后，每次登录强制播放。
	if got, want := segmentOf(40), segmentOf(90); got == want || got == 0 || want == 0 {
		t.Fatalf("real segments for 40/90 = %d/%d, want two different defined segments", got, want)
	}

	for _, test := range []struct {
		name           string
		characterLevel int
		characterStory int64
		accountViewed  uint32
	}{
		{name: "account covers gm-raised level", characterLevel: 90, characterStory: 40, accountViewed: 90},
		{name: "account above character level", characterLevel: 40, characterStory: 40, accountViewed: 90},
		{name: "account exactly at character level", characterLevel: 17, characterStory: 0, accountViewed: 17},
		{name: "account at final segment", characterLevel: 999, characterStory: 1, accountViewed: 999},
	} {
		t.Run(test.name, func(t *testing.T) {
			character := dnfrepo.CharacterRecord{
				Level: test.characterLevel,
				Stats: map[string]int64{
					dnfrepo.CharacterStoryDigestLastLevelStatKey: test.characterStory,
				},
			}
			sent := resolveCurrentStoryDigestSendLevel(character, true, test.accountViewed)
			if sent != uint32(test.characterLevel) {
				t.Fatalf("sent level = %d, want character level %d", sent, test.characterLevel)
			}
			if got, want := segmentOf(int64(sent)), segmentOf(int64(test.characterLevel)); got != want {
				t.Fatalf("real story digest segment sent=%d char=%d: %d vs %d, client would replay", sent, test.characterLevel, got, want)
			}
		})
	}

	// 没有账号级记录时保留旧行为：只按角色自己的记录下发，客户端仍会判落后。
	character := dnfrepo.CharacterRecord{
		Level: 90,
		Stats: map[string]int64{dnfrepo.CharacterStoryDigestLastLevelStatKey: 40},
	}
	if sent := resolveCurrentStoryDigestSendLevel(character, true, 0); sent != 40 {
		t.Fatalf("legacy sent level without account record = %d, want 40", sent)
	}
}

func loadRealStoryDigestTables(t *testing.T) ([]realStoryDigestSegment, map[int64][]string) {
	t.Helper()
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	digestText, err := archive.ReadText(realStoryDigestDocPath)
	if err != nil {
		t.Fatalf("read %s: %v", realStoryDigestDocPath, err)
	}
	sceneText, err := archive.ReadText(realStoryDigestScenePath)
	if err != nil {
		t.Fatalf("read %s: %v", realStoryDigestScenePath, err)
	}
	return parseRealStoryDigestSegments(t, digestText), parseRealStoryDigestScenes(t, sceneText)
}

func parseRealStoryDigestSegments(t *testing.T, text string) []realStoryDigestSegment {
	t.Helper()
	document, err := dnfpvf.Parse(realStoryDigestDocPath, text)
	if err != nil {
		t.Fatal(err)
	}
	tokens := document.Tokens
	segments := make([]realStoryDigestSegment, 0, 17)
	pendingIndex := -1
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if token.Kind != dnfpvf.TokenSection {
			continue
		}
		switch token.Value {
		case "level":
			segment := realStoryDigestSegment{id: realStoryDigestEntryID(tokens, index)}
			if index+2 < len(tokens) && tokens[index+1].Kind == dnfpvf.TokenInt && tokens[index+2].Kind == dnfpvf.TokenInt {
				segment.min, segment.max = tokens[index+1].Int, tokens[index+2].Int
			}
			segments = append(segments, segment)
			pendingIndex = len(segments) - 1
		case "ending":
			if pendingIndex >= 0 && index+1 < len(tokens) && tokens[index+1].Kind == dnfpvf.TokenInt {
				segments[pendingIndex].ending = tokens[index+1].Int
			}
		}
	}
	return segments
}

// realStoryDigestEntryID 取 [level] 之前的那个字符串 token（“ `1` “ … “ `17` “）。
func realStoryDigestEntryID(tokens []dnfpvf.Token, levelIndex int) int64 {
	for back := levelIndex - 1; back >= 0; back-- {
		switch tokens[back].Kind {
		case dnfpvf.TokenString:
			parsed, err := strconv.ParseInt(strings.TrimSpace(tokens[back].Value), 10, 64)
			if err != nil {
				return 0
			}
			return parsed
		case dnfpvf.TokenSection:
			return 0
		}
	}
	return 0
}

// parseRealStoryDigestScenes 返回 scene.etc 里每个 [scene] 块的全部字符串值。
func parseRealStoryDigestScenes(t *testing.T, text string) map[int64][]string {
	t.Helper()
	document, err := dnfpvf.Parse(realStoryDigestScenePath, text)
	if err != nil {
		t.Fatal(err)
	}
	tokens := document.Tokens
	scenes := make(map[int64][]string)
	currentID := int64(0)
	inScene := false
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if token.Kind == dnfpvf.TokenSection {
			switch token.Value {
			case "scene":
				inScene = false
				currentID = 0
				if index+1 < len(tokens) && tokens[index+1].Kind == dnfpvf.TokenInt {
					currentID, inScene = tokens[index+1].Int, true
				}
			case "/scene":
				inScene = false
				currentID = 0
			}
			continue
		}
		if !inScene || token.Kind != dnfpvf.TokenString {
			continue
		}
		if value := strings.TrimSpace(token.Value); value != "" {
			scenes[currentID] = append(scenes[currentID], value)
		}
	}
	return scenes
}

func realStoryDigestSegmentIDForLevel(segments []realStoryDigestSegment, level int64) int64 {
	for _, segment := range segments {
		if level >= segment.min && level <= segment.max {
			return segment.id
		}
	}
	return 0
}
