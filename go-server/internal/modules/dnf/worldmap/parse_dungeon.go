package worldmap

import (
	"fmt"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

func ParseDungeon(id int64, dungeonPath string, doc *dnfpvf.Document) Dungeon {
	result := Dungeon{ID: id, Path: dungeonPath}
	sections, diagnostics := rawSections(doc)
	result.SourceSections = sections
	result.Diagnostics = append(result.Diagnostics, diagnostics...)

	currentMaze := -1
	complexBlock := ""
	for _, section := range sections {
		key := sectionKey(section.Name)
		// [story mode] is a metadata block even when it appears after a maze.
		// Its children are consumed as a unit below; do not let them fall into
		// parseDungeonExtension or the active maze.
		if storyModeOwned(section.Scope) {
			continue
		}
		if key == "maze info" {
			result.Mazes = append(result.Mazes, Maze{Index: len(result.Mazes)})
			currentMaze = len(result.Mazes) - 1
			result.Mazes[currentMaze].SourceSections = append(result.Mazes[currentMaze].SourceSections, cloneRawSection(section))
			continue
		}
		if currentMaze >= 0 && complexBlock != "" {
			parseMazeSection(&result.Mazes[currentMaze], section)
			if key == "/"+complexBlock {
				complexBlock = ""
			}
			continue
		}
		if currentMaze >= 0 && scopeOwnedByDungeonMetadata(section.Scope) {
			parseDungeonExtension(&result, section, sections)
			continue
		}
		if currentMaze >= 0 && (key == "randomized object creation" || key == "clear condition") {
			complexBlock = key
			parseMazeSection(&result.Mazes[currentMaze], section)
			continue
		}
		if currentMaze >= 0 && !isDungeonMetadataSection(key) {
			parseMazeSection(&result.Mazes[currentMaze], section)
			continue
		}
		parseDungeonMetadata(&result, section, sections)
	}
	for index := range result.Mazes {
		parseMazeComplexSections(&result.Mazes[index])
	}
	return result
}

func scopeOwnedByDungeonMetadata(scope []string) bool {
	for _, name := range scope {
		if isDungeonMetadataSection(name) {
			return true
		}
	}
	return false
}

func parseDungeonExtension(result *Dungeon, section RawSection, sections []RawSection) {
	if isClosingSection(section.Name) {
		return
	}
	// `[reward item rate]` 在真档里是 `[result card]` 的**子节点**
	// （厂商 DungeonFile.cs:700-718 用 node.GetChildren("reward item rate") 取），
	// 因此它走的是 scope-owned 这条分支而不是 parseDungeonMetadata。
	if sectionKey(section.Name) == rewardItemRateSectionKey {
		parseDungeonRewardItemRate(result, directBlockSection(sections, section))
		return
	}
	result.UnknownSections = append(result.UnknownSections, cloneRawSection(section))
	result.Extensions = append(result.Extensions, structureExtension(section))
}

// parseDungeonRewardItemRate 累积 `[reward item rate]`（厂商 DungeonFile.cs:700-734：
// 嵌套与独立两种出现方式都累加到同一张表）。
func parseDungeonRewardItemRate(result *Dungeon, section RawSection) {
	rates, ok := parseTournamentRewardItemRates(section)
	if !ok {
		result.Metadata.TournamentRewardItemRateMalformed = true
		return
	}
	result.Metadata.TournamentRewardItemRates = append(result.Metadata.TournamentRewardItemRates, rates...)
}

// parseDungeonRequiredItemFamily 处理 `[required item]` / `[event required item]` /
// `[added required item]`（厂商 DungeonFile.cs:642-657）。
func parseDungeonRequiredItemFamily(result *Dungeon, key string, section RawSection) {
	items, ok := parseRequiredItemRows(section)
	if !ok {
		// 非整数内容：保持"本段不进任何结构化字段"的既有结果，不额外造诊断
		// （厂商 ParseIntArray 对非整数整段放弃，且不产生诊断）。
		return
	}
	switch key {
	case "required item":
		result.Metadata.RequiredItems = append(result.Metadata.RequiredItems, items...)
	case "event required item":
		result.Metadata.EventRequiredItems = append(result.Metadata.EventRequiredItems, items...)
	case "added required item":
		result.Metadata.AddedRequiredItems = append(result.Metadata.AddedRequiredItems, items...)
	}
}

const storyModeSectionKey = "story mode"

func storyModeOwned(scope []string) bool {
	for _, name := range scope {
		if sectionKey(name) == storyModeSectionKey {
			return true
		}
	}
	return false
}

func storyModeDirectChild(section RawSection, parent string) bool {
	if len(section.Scope) == 0 {
		return false
	}
	return sectionKey(section.Scope[len(section.Scope)-1]) == sectionKey(parent)
}

// parseDungeonStoryMode implements the typed subset of PvfLib
// DungeonFile.cs:1584-1661. The flat section model exposes nested blocks through
// Scope, so only direct [story mode] children are read here.
func parseDungeonStoryMode(result *Dungeon, section RawSection, sections []RawSection) {
	if result.Metadata.StoryMode != nil {
		// A duplicate top-level block is ambiguous and must not grant a bonus.
		result.Metadata.StoryMode.Malformed = true
		return
	}
	mode := &DungeonStoryMode{}
	result.Metadata.StoryMode = mode
	if !section.Block {
		mode.Malformed = true
		return
	}
	var difficultySeen, ratesSeen, questsSeen bool
	for _, child := range sections {
		if !storyModeDirectChild(child, storyModeSectionKey) || isClosingSection(child.Name) {
			continue
		}
		switch sectionKey(child.Name) {
		case "difficulty size":
			if difficultySeen {
				mode.Malformed = true
				continue
			}
			difficultySeen = true
			values, ok := storyModeIntegers(child)
			if !ok || len(values) != 1 || values[0] <= 0 {
				mode.Malformed = true
				continue
			}
			mode.DifficultySize = values[0]
		case "increase exp rate":
			if ratesSeen {
				mode.Malformed = true
				continue
			}
			ratesSeen = true
			values, ok := storyModeIntegers(child)
			if !ok {
				mode.Malformed = true
				continue
			}
			for _, value := range values {
				if value < 0 {
					mode.Malformed = true
				}
			}
			mode.IncreaseExperienceRates = append([]int64(nil), values...)
		case "quest list":
			if questsSeen {
				mode.Malformed = true
				continue
			}
			questsSeen = true
			parseStoryModeQuestList(mode, child)
		}
	}
	if !difficultySeen || !ratesSeen || !questsSeen {
		mode.Malformed = true
	}
	// The current real Script.pvf contains 176 [story mode] blocks; all declare
	// difficulty size 1 with two integer increase-exp values. Preserve those
	// declared values and validate only the observed shape from parsed integers;
	// the bridge guard owns which index is eligible.
	if mode.DifficultySize <= 0 || len(mode.IncreaseExperienceRates) != int(mode.DifficultySize)+1 {
		mode.Malformed = true
	}
	preserveUnknownStoryModeSections(result, sections)
}

// preserveUnknownStoryModeSections keeps future/unknown story-mode children,
// including [independent rate], in the dungeon's raw extension inventory.
// They must not be interpreted as maze sections or approved quests, but their
// data still remains available for diagnostics.
func preserveUnknownStoryModeSections(result *Dungeon, sections []RawSection) {
	for _, section := range sections {
		if !storyModeOwned(section.Scope) || isClosingSection(section.Name) {
			continue
		}
		switch sectionKey(section.Name) {
		case "difficulty size", "increase exp rate", "quest list":
			continue
		}
		result.UnknownSections = append(result.UnknownSections, cloneRawSection(section))
		result.Extensions = append(result.Extensions, structureExtension(section))
	}
}

func parseStoryModeQuestList(mode *DungeonStoryMode, section RawSection) {
	values, ok := storyModeIntegers(section)
	if !ok || len(values) == 0 {
		mode.Malformed = true
	}
	seen := make(map[int64]struct{}, len(values))
	for _, questID := range values {
		if questID <= 0 {
			mode.Malformed = true
			continue
		}
		if _, duplicate := seen[questID]; duplicate {
			mode.Malformed = true
			continue
		}
		seen[questID] = struct{}{}
		mode.QuestIDs = append(mode.QuestIDs, questID)
	}
}

// storyModeIntegers removes brace sections (the real quest list contains
// {7=...} markers) and requires every remaining token to be an integer.
func storyModeIntegers(section RawSection) ([]int64, bool) {
	depth := 0
	out := make([]int64, 0, len(section.Tokens))
	for _, token := range section.Tokens {
		switch token.Kind {
		case dnfpvf.TokenSymbol:
			switch token.Value {
			case "{":
				depth++
			case "}":
				if depth == 0 {
					return nil, false
				}
				depth--
			default:
				if depth == 0 {
					return nil, false
				}
			}
		case dnfpvf.TokenInt:
			if depth == 0 {
				out = append(out, token.Int)
			}
		default:
			if depth == 0 {
				return nil, false
			}
		}
	}
	if depth != 0 {
		return nil, false
	}
	return out, true
}

func parseDungeonMetadata(result *Dungeon, section RawSection, sections []RawSection) {
	key := sectionKey(section.Name)
	if isClosingSection(key) {
		return
	}
	// 结构化段必须在 opaque/block 分支之前分流：真档里 `[required item]` 是
	// `[required item] … [/required item]` 的 block 形式（section.Block == true），
	// `[result card]` / `[tournament clear reward exp]` 同样是 block。
	for _, requiredKey := range requiredItemSectionKeys {
		if key == requiredKey {
			parseDungeonRequiredItemFamily(result, key, directBlockSection(sections, section))
			return
		}
	}
	switch key {
	case storyModeSectionKey:
		parseDungeonStoryMode(result, section, sections)
		return
	case resultCardSectionKey:
		cards, ok := parseTournamentResultCards(directBlockSection(sections, section))
		if !ok {
			result.Metadata.TournamentResultCardMalformed = true
			return
		}
		result.Metadata.TournamentResultCards = append(result.Metadata.TournamentResultCards, cards...)
		return
	case rewardItemRateSectionKey:
		parseDungeonRewardItemRate(result, directBlockSection(sections, section))
		return
	case tournamentClearRewardExpSectionKey:
		experiences, ok := parseTournamentRewardExperiences(directBlockSection(sections, section))
		if !ok {
			result.Metadata.TournamentRewardExpMalformed = true
			return
		}
		result.Metadata.TournamentClearExperiences = append(result.Metadata.TournamentClearExperiences, experiences...)
		return
	case DungeonUseFatigueOnlyStartKey:
		parseDungeonStartFatigueCost(result, section, sections)
		return
	}
	if section.Block {
		result.OpaqueSections = append(result.OpaqueSections, cloneRawSection(section))
		result.Extensions = append(result.Extensions, structureExtension(section))
		return
	}
	if _, ok := opaqueDungeonSections[key]; ok {
		result.OpaqueSections = append(result.OpaqueSections, cloneRawSection(section))
		return
	}
	if _, ok := dungeonIntegerSections[key]; ok {
		value, ok := integerMetadataValue(key, section)
		if !ok {
			result.Diagnostics = append(result.Diagnostics, diagnostic(section, "expected integer metadata value"))
			return
		}
		if result.Metadata.IntegerValues == nil {
			result.Metadata.IntegerValues = make(map[string]int64)
		}
		result.Metadata.IntegerValues[key] = value
		return
	}
	if _, ok := dungeonNumberSections[key]; ok {
		value, ok := firstNumber(section)
		if !ok {
			result.Diagnostics = append(result.Diagnostics, diagnostic(section, "expected numeric metadata value"))
			return
		}
		if result.Metadata.NumberValues == nil {
			result.Metadata.NumberValues = make(map[string]float64)
		}
		result.Metadata.NumberValues[key] = value
		return
	}
	if _, ok := dungeonFlagSections[key]; ok {
		if result.Metadata.Flags == nil {
			result.Metadata.Flags = make(map[string]bool)
		}
		result.Metadata.Flags[key] = true
		return
	}
	if _, ok := dungeonTextSections[key]; ok {
		parsed := texts(section)
		if len(parsed) == 0 {
			result.Diagnostics = append(result.Diagnostics, diagnostic(section, "expected text metadata value"))
			return
		}
		if result.Metadata.TextValues == nil {
			result.Metadata.TextValues = make(map[string][]string)
		}
		result.Metadata.TextValues[key] = append(result.Metadata.TextValues[key], parsed...)
		return
	}
	if _, ok := dungeonCoreSections[key]; !ok {
		result.UnknownSections = append(result.UnknownSections, cloneRawSection(section))
		result.Extensions = append(result.Extensions, structureExtension(section))
		return
	}

	switch key {
	case "name":
		setText(&result.Metadata.Name, section, &result.Diagnostics)
	case "explain":
		setText(&result.Metadata.Explain, section, &result.Diagnostics)
	case "cutscene image":
		if value, ok := firstText(section); ok {
			result.Metadata.CutsceneImage.Path = value
		} else {
			result.Diagnostics = append(result.Diagnostics, diagnostic(section, "expected cutscene image path"))
		}
		parsed, _ := ints(section)
		result.Metadata.CutsceneImage.Params = append(result.Metadata.CutsceneImage.Params, parsed...)
	case "minimap image":
		setText(&result.Metadata.MinimapImage, section, &result.Diagnostics)
	case "entering title":
		setText(&result.Metadata.EnteringTitle, section, &result.Diagnostics)
	case "minimum required level":
		setOptionalInt(&result.Metadata.MinimumRequiredLevel, section, &result.Diagnostics)
	case "basis level":
		setOptionalInt(&result.Metadata.BasisLevel, section, &result.Diagnostics)
	case "experience increasing point":
		setOptionalNumber(&result.Metadata.ExperienceIncreasingPoint, section, &result.Diagnostics)
	case "background pos":
		setOptionalInt(&result.Metadata.BackgroundPosition, section, &result.Diagnostics)
	case "dungeon type":
		setText(&result.Metadata.DungeonType, section, &result.Diagnostics)
	case "champion":
		result.Metadata.Champion = appendInts(result.Metadata.Champion, section, &result.Diagnostics)
	case "pathgate object":
		result.Metadata.PathgateObjects = appendInts(result.Metadata.PathgateObjects, section, &result.Diagnostics)
	case "worldmap pattern info":
		result.Metadata.WorldMapPatternInfo = append(result.Metadata.WorldMapPatternInfo, cloneRawSection(section))
	case "worldmap info":
		result.Metadata.WorldMapInfo = append(result.Metadata.WorldMapInfo, cloneRawSection(section))
	case "difficulty":
		setOptionalInt(&result.Metadata.Difficulty, section, &result.Diagnostics)
	case "difficulty level":
		result.Metadata.DifficultyLevels = appendInts(result.Metadata.DifficultyLevels, section, &result.Diagnostics)
	case "designate dungeon difficulty":
		result.Metadata.DesignatedDifficulties = appendInts(result.Metadata.DesignatedDifficulties, section, &result.Diagnostics)
	case "tutorial dungeon":
		setOptionalInt(&result.Metadata.TutorialDungeon, section, &result.Diagnostics)
	case "no fatigue":
		result.Metadata.NoFatigue = true
	case "named monster":
		result.Metadata.NamedMonsters = appendInts(result.Metadata.NamedMonsters, section, &result.Diagnostics)
	case "recommended level":
		result.Metadata.RecommendedLevels = appendInts(result.Metadata.RecommendedLevels, section, &result.Diagnostics)
	case "limit party count":
		setOptionalInt(&result.Metadata.LimitPartyCount, section, &result.Diagnostics)
	}
}

// parseDungeonStartFatigueCost preserves both observed forms of this metadata:
// a modern block with an integer cost and the legacy bare presence marker.
func parseDungeonStartFatigueCost(result *Dungeon, section RawSection, sections []RawSection) {
	merged := directBlockSection(sections, section)
	parsed, valid := ints(merged)
	if valid && len(parsed) == 1 {
		if result.Metadata.IntegerValues == nil {
			result.Metadata.IntegerValues = make(map[string]int64)
		}
		result.Metadata.IntegerValues[DungeonUseFatigueOnlyStartKey] = parsed[0]
		return
	}
	if !section.Block && len(merged.Tokens) == 0 {
		if result.Metadata.Flags == nil {
			result.Metadata.Flags = make(map[string]bool)
		}
		result.Metadata.Flags[DungeonUseFatigueOnlyStartKey] = true
		return
	}
	result.Diagnostics = append(result.Diagnostics, diagnostic(section, "expected one integer start dungeon fatigue cost"))
}

const (
	towerOfDespairSectionKey = "tower of despair"
	towerOfGriefSectionKey   = "tower of grief"
)

// Real despair and grief scripts use a bare presence marker. Despair also
// follows DungeonFile.cs:570-571. Explicit integers keep their value.
// Symbols and other nonempty invalid
// values, and empty sections of other kinds, retain the existing diagnostic.
func integerMetadataValue(key string, section RawSection) (int64, bool) {
	if value, ok := firstInt(section); ok {
		return value, true
	}
	if (key == towerOfDespairSectionKey || key == towerOfGriefSectionKey) && len(section.Tokens) == 0 {
		return 1, true
	}
	return 0, false
}

func parseMazeSection(maze *Maze, section RawSection) {
	maze.SourceSections = append(maze.SourceSections, cloneRawSection(section))
	key := sectionKey(section.Name)
	if isClosingSection(key) {
		return
	}
	if _, ok := knownMazeSections[key]; !ok {
		maze.UnknownSections = append(maze.UnknownSections, cloneRawSection(section))
		maze.Extensions = append(maze.Extensions, structureExtension(section))
		return
	}
	if _, ok := opaqueMazeSections[key]; ok {
		maze.OpaqueSections = append(maze.OpaqueSections, cloneRawSection(section))
		switch key {
		case "boss map specification":
			maze.BossMapSpecifications = append(maze.BossMapSpecifications, cloneRawSection(section))
		case "layered map specification":
			maze.LayeredMapSpecifications = append(maze.LayeredMapSpecifications, cloneRawSection(section))
		}
		return
	}
	switch key {
	case "hidden boss maze":
		maze.HiddenBoss = true
	case "size":
		parsed, valid := ints(section)
		if !valid || len(parsed) < 2 {
			maze.Diagnostics = append(maze.Diagnostics, diagnostic(section, "expected maze width and height"))
			return
		}
		maze.Width = OptionalInt{Value: parsed[0], Set: true}
		maze.Height = OptionalInt{Value: parsed[1], Set: true}
		if len(parsed) > 2 {
			maze.Diagnostics = append(maze.Diagnostics, diagnostic(section, "maze size has trailing values"))
		}
	case "greed":
		setText(&maze.Greed, section, &maze.Diagnostics)
	case "map specification":
		maze.MapSpecifications = append(maze.MapSpecifications, parseMapSpecifications(section, &maze.Diagnostics)...)
	case "start map":
		maze.Start = parseMazePoint(section, &maze.Diagnostics)
	case "boss map":
		maze.Boss = parseMazePoint(section, &maze.Diagnostics)
	case "hit count":
		maze.HitCount = appendInts(maze.HitCount, section, &maze.Diagnostics)
	case "seal door appear rate":
		setOptionalInt(&maze.SealDoorAppearRate, section, &maze.Diagnostics)
	case "seal door map index":
		setOptionalInt(&maze.SealDoorMapIndex, section, &maze.Diagnostics)
	case "seal door pos":
		maze.SealDoorPosition = appendInts(maze.SealDoorPosition, section, &maze.Diagnostics)
	case "quest connection":
		maze.QuestConnection = appendInts(maze.QuestConnection, section, &maze.Diagnostics)
	}
}

func parseMapSpecifications(section RawSection, diagnostics *[]Diagnostic) []MapSpecification {
	tokens := values(section)
	var out []MapSpecification
	for i := 0; i < len(tokens); {
		if !isText(tokens[i]) {
			*diagnostics = append(*diagnostics, diagnostic(section, fmt.Sprintf("expected specification type at token %d", i)))
			i++
			continue
		}
		if i+3 >= len(tokens) || !areInts(tokens[i+1:i+4]) {
			*diagnostics = append(*diagnostics, diagnostic(section, fmt.Sprintf("malformed map specification at token %d", i)))
			i++
			continue
		}
		spec := MapSpecification{
			Type:              tokens[i].Value,
			Coordinate:        Point{X: tokens[i+1].Int, Y: tokens[i+2].Int},
			MapIDs:            []int64{tokens[i+3].Int},
			SectionOccurrence: section.Occurrence,
		}
		i += 4
		for i < len(tokens) && tokens[i].Kind == dnfpvf.TokenInt {
			spec.MapIDs = append(spec.MapIDs, tokens[i].Int)
			i++
		}
		out = append(out, spec)
	}
	return out
}

func parseMazeComplexSections(maze *Maze) {
	for pos := 0; pos < len(maze.SourceSections); pos++ {
		section := maze.SourceSections[pos]
		switch sectionKey(section.Name) {
		case "boss map specification":
			maze.BossSpecifications = append(
				maze.BossSpecifications,
				parseTypedOrImplicitSpecifications(section, "boss", &maze.Diagnostics)...,
			)
		case "layered map specification":
			maze.LayeredSpecifications = append(
				maze.LayeredSpecifications,
				parseTypedOrImplicitSpecifications(section, "layered", &maze.Diagnostics)...,
			)
		case "randomized object creation":
			script, next := parseRandomizedObjectScript(maze.SourceSections, pos, &maze.Diagnostics)
			maze.RandomizedObjects = append(maze.RandomizedObjects, script)
			pos = next
		case "clear condition":
			conditions, next := parseClearConditions(maze.SourceSections, pos, &maze.Diagnostics)
			maze.ClearConditions = append(maze.ClearConditions, conditions...)
			pos = next
		}
	}
}

func parseTypedOrImplicitSpecifications(section RawSection, implicitType string, diagnostics *[]Diagnostic) []MapSpecification {
	tokens := values(section)
	if len(tokens) > 0 && isText(tokens[0]) {
		return parseMapSpecifications(section, diagnostics)
	}
	if len(tokens) < 3 || !areInts(tokens[:3]) {
		*diagnostics = append(*diagnostics, diagnostic(section, "expected x/y/map-id specification values"))
		return nil
	}
	mapIDs := make([]int64, 0, len(tokens)-2)
	for _, token := range tokens[2:] {
		if token.Kind != dnfpvf.TokenInt {
			*diagnostics = append(*diagnostics, diagnostic(section, "implicit specification contains non-integer map id"))
			return nil
		}
		mapIDs = append(mapIDs, token.Int)
	}
	return []MapSpecification{{
		Type: implicitType, Coordinate: Point{X: tokens[0].Int, Y: tokens[1].Int},
		MapIDs: mapIDs, SectionOccurrence: section.Occurrence,
	}}
}

func parseRandomizedObjectScript(sections []RawSection, start int, diagnostics *[]Diagnostic) (RandomizedObjectScript, int) {
	script := RandomizedObjectScript{}
	end := start
	for pos := start; pos < len(sections); pos++ {
		section := sections[pos]
		key := sectionKey(section.Name)
		script.SourceSections = append(script.SourceSections, cloneRawSection(section))
		end = pos
		if key == "/randomized object creation" {
			break
		}
		switch key {
		case "randomized object creation":
		case "select":
			setOptionalInt(&script.SelectCount, section, diagnostics)
		case "regenerate":
			setOptionalInt(&script.Regenerate, section, diagnostics)
		case "minimap icon":
			setOptionalInt(&script.MinimapIcon, section, diagnostics)
		case "object":
			object, objectEnd := parseRidableObject(sections, pos, diagnostics)
			script.Objects = append(script.Objects, object)
			for nested := pos + 1; nested <= objectEnd && nested < len(sections); nested++ {
				script.SourceSections = append(script.SourceSections, cloneRawSection(sections[nested]))
			}
			pos = objectEnd
			end = objectEnd
		default:
			if !isClosingSection(key) {
				script.UnknownSections = append(script.UnknownSections, cloneRawSection(section))
				script.Extensions = append(script.Extensions, structureExtension(section))
			}
		}
	}
	return script, end
}

func parseRidableObject(sections []RawSection, start int, diagnostics *[]Diagnostic) (RidableObject, int) {
	object := RidableObject{}
	end := start
	for pos := start; pos < len(sections); pos++ {
		section := sections[pos]
		key := sectionKey(section.Name)
		object.SourceSections = append(object.SourceSections, cloneRawSection(section))
		end = pos
		if key == "/object" {
			break
		}
		switch key {
		case "object", "team":
		case "object type":
			if value, ok := firstText(section); ok {
				object.ObjectType = value
			} else {
				*diagnostics = append(*diagnostics, diagnostic(section, "expected explicit randomized object type"))
			}
		case "map":
			object.MazeCoordinate = parsePoint(section, "maze map coordinate", diagnostics)
		case "index":
			setOptionalInt(&object.ObjectIndex, section, diagnostics)
		case "pos":
			object.Position = parsePoint(section, "object position", diagnostics)
		case "monster", "neutral", "character":
			object.Team = key
		default:
			if !isClosingSection(key) {
				object.UnknownSections = append(object.UnknownSections, cloneRawSection(section))
				object.Extensions = append(object.Extensions, structureExtension(section))
			}
		}
	}
	return object, end
}

func parseClearConditions(sections []RawSection, start int, diagnostics *[]Diagnostic) ([]ClearCondition, int) {
	var out []ClearCondition
	end := start
	for pos := start + 1; pos < len(sections); pos++ {
		section := sections[pos]
		key := sectionKey(section.Name)
		end = pos
		if key == "/clear condition" {
			break
		}
		switch key {
		case "destroy object", "seeking", "hunt monster", "hunt apc", "hunt boss":
			parsed, valid := ints(section)
			if !valid || len(parsed)%2 != 0 {
				*diagnostics = append(*diagnostics, diagnostic(section, "expected clear-condition target/count pairs"))
			}
			for index := 0; index+1 < len(parsed); index += 2 {
				out = append(out, ClearCondition{Type: key, TargetID: parsed[index], Count: parsed[index+1]})
			}
		}
	}
	return out, end
}

func parsePoint(section RawSection, label string, diagnostics *[]Diagnostic) *Point {
	parsed, valid := ints(section)
	if !valid || len(parsed) < 2 {
		*diagnostics = append(*diagnostics, diagnostic(section, "expected "+label+" x/y values"))
		return nil
	}
	if len(parsed) > 2 {
		*diagnostics = append(*diagnostics, diagnostic(section, label+" has trailing values"))
	}
	return &Point{X: parsed[0], Y: parsed[1]}
}

func parseMazePoint(section RawSection, diagnostics *[]Diagnostic) *MazePoint {
	parsed, valid := ints(section)
	if !valid || len(parsed) < 2 {
		*diagnostics = append(*diagnostics, diagnostic(section, "expected maze x/y coordinates"))
		return nil
	}
	return &MazePoint{X: parsed[0], Y: parsed[1], Params: append([]int64(nil), parsed[2:]...)}
}

func setOptionalNumber(target *OptionalNumber, section RawSection, diagnostics *[]Diagnostic) {
	value, ok := firstNumber(section)
	if !ok {
		*diagnostics = append(*diagnostics, diagnostic(section, "expected numeric value"))
		return
	}
	target.Value = value
	target.Set = true
}

func firstNumber(section RawSection) (float64, bool) {
	for _, token := range section.Tokens {
		switch token.Kind {
		case dnfpvf.TokenInt:
			return float64(token.Int), true
		case dnfpvf.TokenFloat:
			return token.Float, true
		}
	}
	return 0, false
}

func structureExtension(section RawSection) ExtensionSection {
	extension := ExtensionSection{
		Name: section.Name, Occurrence: section.Occurrence,
		Scope: append([]string(nil), section.Scope...), Block: section.Block,
		Tokens: append([]dnfpvf.Token(nil), section.Tokens...),
	}
	nonSymbols := 0
	allIntegers := true
	allNumbers := true
	allTexts := true
	for _, token := range section.Tokens {
		switch token.Kind {
		case dnfpvf.TokenInt:
			nonSymbols++
			extension.Integers = append(extension.Integers, token.Int)
			extension.Numbers = append(extension.Numbers, float64(token.Int))
			allTexts = false
		case dnfpvf.TokenFloat:
			nonSymbols++
			extension.Numbers = append(extension.Numbers, token.Float)
			allIntegers = false
			allTexts = false
		case dnfpvf.TokenString, dnfpvf.TokenIdent:
			nonSymbols++
			extension.Texts = append(extension.Texts, token.Value)
			allIntegers = false
			allNumbers = false
		case dnfpvf.TokenSymbol:
			extension.Symbols = append(extension.Symbols, token.Value)
		}
	}
	switch {
	case nonSymbols == 0 && len(extension.Symbols) == 0:
		extension.Kind = SectionFlag
	case allIntegers:
		extension.Kind = SectionIntegers
	case allNumbers:
		extension.Kind = SectionNumbers
	case allTexts:
		extension.Kind = SectionTexts
	default:
		extension.Kind = SectionMixed
	}
	return extension
}
