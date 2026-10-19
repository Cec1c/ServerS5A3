package dnfbridge

import (
	"context"
	"sort"
	"strconv"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// The caller has proved the exact announced hostile APC, owned room, current
// run and owner-ffff/no-combat request. An opening cinematic can explicitly
// destroy that APC in an intermediate quest room as well as in the boss room.
// This recognizes a client report; it does not run the movie or synthesize it.
func (s *Service) currentDungeonCinematicAPCDestroy(ctx context.Context, session *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene, actor runtimeDungeonExtendedActor, quests *dnfquest.Catalog) (dungeonStoryProcDeathEvidence, bool) {
	if runtime.MazeIndex < 0 || runtime.MazeIndex >= len(runtime.Dungeon.Mazes) || actor.AICharacter == nil || actor.HostileReference == nil {
		return dungeonStoryProcDeathEvidence{}, false
	}
	connection := runtime.Dungeon.Mazes[runtime.MazeIndex].QuestConnection
	if len(connection) != 3 || connection[0] != 0 || connection[1] <= 0 {
		return dungeonStoryProcDeathEvidence{}, false
	}
	questID := connection[1]
	definition, found := quests.Find(questID)
	if !found || len(definition.IntData) != 1 || !currentDungeonConnectedClearMapQuestOwnsBaseMap(runtime, definition.IntData[0], -1, quests) {
		return dungeonStoryProcDeathEvidence{}, false
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Quest == nil {
		return dungeonStoryProcDeathEvidence{}, false
	}
	record, found, err := repos.Quest.Load(ctx, strconv.FormatUint(uint64(session.selectedCharacterID), 10))
	if err != nil || !found {
		return dungeonStoryProcDeathEvidence{}, false
	}
	state, found := record.States[questID]
	if !found {
		state, found = record.Progress[questID]
	}
	if !found || state.Status != "active" || state.ProgressValue <= 0 {
		return dungeonStoryProcDeathEvidence{}, false
	}
	catalog, err := s.dungeonAICharacterCatalog()
	if err != nil || catalog.source == nil {
		return dungeonStoryProcDeathEvidence{}, false
	}
	ai, found, err := catalog.Find(actor.AICharacter.Code)
	if err != nil || !found || actor.AICharacterMetadata == nil || ai.Path != actor.AICharacterMetadata.Path {
		return dungeonStoryProcDeathEvidence{}, false
	}
	actionPath := resolveDungeonBasicActionPath(scene.Map.Map.Path, scene.Map.Map.BasicAction)
	if scene.Map.Map.BasicAction == "" || !strings.HasPrefix(normalizeDungeonRuntimePath(actionPath), "map/") {
		return dungeonStoryProcDeathEvidence{}, false
	}
	text, err := catalog.source.ReadText(actionPath)
	if err != nil {
		return dungeonStoryProcDeathEvidence{}, false
	}
	doc, err := worldmap.ParseDocument(actionPath, text)
	if err != nil {
		return dungeonStoryProcDeathEvidence{}, false
	}
	ids := dungeonOnStartCinematics(doc)
	if len(ids) == 0 {
		return dungeonStoryProcDeathEvidence{}, false
	}
	listText, err := catalog.source.ReadText(defaultDungeonCinematicList)
	if err != nil {
		return dungeonStoryProcDeathEvidence{}, false
	}
	list, err := dnfpvf.Parse(defaultDungeonCinematicList, listText)
	if err != nil {
		return dungeonStoryProcDeathEvidence{}, false
	}
	paths := make(map[int64]string)
	for _, entry := range dnfpvf.ParseList(list) {
		if previous, ok := paths[entry.ID]; ok && previous != entry.Path {
			return dungeonStoryProcDeathEvidence{}, false
		}
		paths[entry.ID] = entry.Path
	}
	for _, id := range ids {
		ref, ok := paths[id]
		if !ok {
			continue
		}
		cinematicPath, text, err := readDungeonCinematicText(catalog.source, ref)
		if err != nil {
			continue
		}
		cinematic, err := worldmap.ParseDocument(cinematicPath, text)
		if err != nil {
			continue
		}
		usage, ok := parseDungeonCinematicAPCDestroyUsage(cinematic, scene.Map.Map.ID)
		if !ok {
			continue
		}
		for _, index := range usage.DestroyAPCIndexes {
			if index == actor.HostileReference.Index {
				return dungeonStoryProcDeathEvidence{QuestID: questID, AICharacterID: ai.ID, ActionPath: cinematicPath, BehaviorIndex: index, CinematicID: id}, true
			}
		}
	}
	return dungeonStoryProcDeathEvidence{}, false
}

type dungeonCinematicAPCDestroyUsage struct {
	MapID             int64
	DestroyAPCIndexes []int
}

// ACT's ON START trigger is flat, so blocks have to be split here rather than
// trusted as a generic section stream. Only the one-shot,
// ON START MAP -> CHARACTER -> ME -> one CINEMATIC behavior link is proved.
// A cinematic merely sharing the same MAP tag is insufficient.
func dungeonOnStartCinematics(doc *dnfpvf.Document) []int64 {
	if doc == nil {
		return nil
	}
	type block struct {
		kind     string
		sections []dnfpvf.Section
	}
	var blocks []block
	var current block
	for _, section := range doc.Sections {
		key := strings.ToLower(strings.TrimSpace(section.Name))
		if section.Start < 0 || section.End < section.Start || section.End > len(doc.Tokens) {
			return nil
		}
		if current.kind == "" {
			if (key != "trigger" && key != "behavior") || section.Start != section.End {
				return nil
			}
			current = block{kind: key}
			continue
		}
		if key == "/"+current.kind {
			if section.Start != section.End {
				return nil
			}
			blocks = append(blocks, current)
			current = block{}
			continue
		}
		current.sections = append(current.sections, section)
	}
	if current.kind != "" {
		return nil
	}
	var refs []int64
	var behaviorIDs []int64
	for _, b := range blocks {
		if b.kind == "behavior" {
			id := int64(0)
			if len(b.sections) == 1 && strings.EqualFold(b.sections[0].Name, "cinematic") {
				values := doc.Tokens[b.sections[0].Start:b.sections[0].End]
				if len(values) == 1 && values[0].Kind == dnfpvf.TokenInt && values[0].Int > 0 {
					id = values[0].Int
				}
			}
			behaviorIDs = append(behaviorIDs, id)
			continue
		}
		want := []string{"on start map", "limit", "which", "character", "do behavior", "me"}
		if len(b.sections) != len(want) {
			continue
		}
		valid := true
		ref := int64(-1)
		for i, section := range b.sections {
			if !strings.EqualFold(section.Name, want[i]) {
				valid = false
				break
			}
			values := doc.Tokens[section.Start:section.End]
			switch i {
			case 1:
				valid = len(values) == 1 && values[0].Kind == dnfpvf.TokenInt && values[0].Int == 1
			case 5:
				valid = len(values) == 1 && values[0].Kind == dnfpvf.TokenInt && values[0].Int >= 0
				if valid {
					ref = values[0].Int
				}
			default:
				valid = len(values) == 0
			}
			if !valid {
				break
			}
		}
		if valid {
			refs = append(refs, ref)
		}
	}
	var ids []int64
	for _, ref := range refs {
		if ref >= 0 && ref < int64(len(behaviorIDs)) && behaviorIDs[ref] > 0 {
			ids = append(ids, behaviorIDs[ref])
		}
	}
	return ids
}

// parseDungeonCinematicAPCDestroyUsage is deliberately narrower than the
// tutorial MONSTER parser. APC and MONSTER indices are separate namespaces,
// and an APC mentioned as a MOVE target is not a DESTROY subject. Each accepted
// behavior is exactly one closed APC actor followed by one closed DESTROY.
func parseDungeonCinematicAPCDestroyUsage(document *dnfpvf.Document, wantMapID int64) (dungeonCinematicAPCDestroyUsage, bool) {
	if document == nil || wantMapID <= 0 {
		return dungeonCinematicAPCDestroyUsage{}, false
	}
	var usage dungeonCinematicAPCDestroyUsage
	mapSeen := false
	sceneSeen := false
	sceneDepth := 0
	destroyIndexes := make(map[int]struct{})
	for index := 0; index < len(document.Sections); index++ {
		section := document.Sections[index]
		if section.Start < 0 || section.End < section.Start || section.End > len(document.Tokens) {
			return dungeonCinematicAPCDestroyUsage{}, false
		}
		key := strings.ToLower(strings.TrimSpace(section.Name))
		switch key {
		case "map":
			if mapSeen || sceneDepth != 0 {
				return dungeonCinematicAPCDestroyUsage{}, false
			}
			tokens := document.Tokens[section.Start:section.End]
			if len(tokens) != 1 || tokens[0].Kind != dnfpvf.TokenInt || tokens[0].Int <= 0 {
				return dungeonCinematicAPCDestroyUsage{}, false
			}
			usage.MapID = tokens[0].Int
			mapSeen = true
		case "scene":
			if sceneDepth != 0 {
				return dungeonCinematicAPCDestroyUsage{}, false
			}
			sceneDepth = 1
			sceneSeen = true
		case "/scene":
			if sceneDepth != 1 {
				return dungeonCinematicAPCDestroyUsage{}, false
			}
			sceneDepth = 0
		case "behavior":
			if sceneDepth != 1 {
				return dungeonCinematicAPCDestroyUsage{}, false
			}
			end := dungeonCinematicMatchingClose(document, index, "behavior")
			if end < 0 {
				return dungeonCinematicAPCDestroyUsage{}, false
			}
			if apcIndex, ok := parseDungeonCinematicAPCDestroyBehavior(document, index, end); ok {
				destroyIndexes[apcIndex] = struct{}{}
			}
			index = end
		case "/behavior":
			return dungeonCinematicAPCDestroyUsage{}, false
		}
	}
	if sceneDepth != 0 || !mapSeen || !sceneSeen || usage.MapID != wantMapID || len(destroyIndexes) == 0 {
		return dungeonCinematicAPCDestroyUsage{}, false
	}
	usage.DestroyAPCIndexes = make([]int, 0, len(destroyIndexes))
	for index := range destroyIndexes {
		usage.DestroyAPCIndexes = append(usage.DestroyAPCIndexes, index)
	}
	sort.Ints(usage.DestroyAPCIndexes)
	return usage, true
}

func dungeonCinematicMatchingClose(document *dnfpvf.Document, start int, kind string) int {
	if document == nil || start < 0 || start >= len(document.Sections) {
		return -1
	}
	depth := 0
	for index := start; index < len(document.Sections); index++ {
		key := strings.ToLower(strings.TrimSpace(document.Sections[index].Name))
		switch key {
		case kind:
			depth++
		case "/" + kind:
			depth--
			if depth == 0 {
				return index
			}
			if depth < 0 {
				return -1
			}
		}
	}
	return -1
}

func parseDungeonCinematicAPCDestroyBehavior(document *dnfpvf.Document, start, end int) (int, bool) {
	actorStart := start + 1
	if actorStart >= end || !strings.EqualFold(document.Sections[actorStart].Name, "actor") {
		return 0, false
	}
	actorEnd := dungeonCinematicMatchingClose(document, actorStart, "actor")
	if actorEnd < 0 || actorEnd >= end {
		return 0, false
	}
	apcIndex, ok := parseDungeonCinematicAPCActor(document, actorStart, actorEnd)
	if !ok {
		return 0, false
	}
	destroyStart := actorEnd + 1
	if destroyStart >= end || !strings.EqualFold(document.Sections[destroyStart].Name, "destroy") {
		return 0, false
	}
	destroyEnd := dungeonCinematicMatchingClose(document, destroyStart, "destroy")
	if destroyEnd < 0 || destroyEnd != end-1 {
		return 0, false
	}
	// The matched native show=0 branch sends an APC death request directly.
	// show=1 uses a separate HP path. Defaults are show=0, reward=1;
	// reward does not alter the show=0 send path.
	seen := make(map[string]bool)
	for index := destroyStart + 1; index < destroyEnd; index++ {
		field := document.Sections[index]
		key := strings.ToLower(strings.TrimSpace(field.Name))
		if (key != "is show die" && key != "is reward") || seen[key] {
			return 0, false
		}
		seen[key] = true
		values := document.Tokens[field.Start:field.End]
		if len(values) != 1 || values[0].Kind != dnfpvf.TokenInt ||
			values[0].Int < 0 || values[0].Int > 1 ||
			(key == "is show die" && values[0].Int != 0) {
			return 0, false
		}
	}
	return apcIndex, true
}

func parseDungeonCinematicAPCActor(document *dnfpvf.Document, start, end int) (int, bool) {
	fields := document.Sections[start+1 : end]
	if len(fields) < 4 ||
		!strings.EqualFold(fields[0].Name, "type") ||
		len(document.Tokens[fields[0].Start:fields[0].End]) != 0 ||
		!strings.EqualFold(fields[1].Name, "apc") ||
		len(document.Tokens[fields[1].Start:fields[1].End]) != 0 ||
		!strings.EqualFold(fields[2].Name, "index") {
		return 0, false
	}
	indexTokens := document.Tokens[fields[2].Start:fields[2].End]
	if len(indexTokens) != 1 || indexTokens[0].Kind != dnfpvf.TokenInt || indexTokens[0].Int < 0 {
		return 0, false
	}
	for index := 3; index < len(fields); index++ {
		if !strings.EqualFold(fields[index].Name, "pos") {
			return 0, false
		}
		tokens := document.Tokens[fields[index].Start:fields[index].End]
		if len(tokens) != 2 || tokens[0].Kind != dnfpvf.TokenInt || tokens[1].Kind != dnfpvf.TokenInt {
			return 0, false
		}
	}
	return int(indexTokens[0].Int), true
}
