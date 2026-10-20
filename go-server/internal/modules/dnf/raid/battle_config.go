package raid

import (
	"fmt"
	"math"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// Dungeon states are consumed by the actual current-client selector: 0 is
// selectable and 2 is hidden. Configuration, rather than bridge constants,
// owns which dungeon is initially available in each phase.
type DungeonState struct {
	ID    uint32
	State byte
}

type BattleConfiguration struct {
	DisconnectWaitSeconds uint32
	StartDelaySeconds     uint32
	PhaseLimitSeconds     []uint32
	PhaseDungeons         [][]DungeonState
	PhaseCoinLimits       map[byte]uint32
	WeeklyClearLimit      int
}

func sectionValues(doc *dnfpvf.Document, section dnfpvf.Section) []dnfpvf.Token {
	return doc.Tokens[section.Start:section.End]
}

func LoadBattleConfiguration(source dnfpvf.Source, path string) (BattleConfiguration, error) {
	text, err := source.ReadText(path)
	if err != nil {
		return BattleConfiguration{}, err
	}
	doc, err := dnfpvf.Parse(path, text)
	if err != nil {
		return BattleConfiguration{}, err
	}
	phaseMax, _ := doc.Int("PHASE MAX")
	delay, _ := doc.Int("START DELAY TIME")
	weekly, _ := doc.Int("WEEKLY CLEAR COUNT")
	times := doc.Ints("PHASE TIME OVER")
	if phaseMax <= 0 || phaseMax > 255 || delay < 0 || delay > 3600 || weekly <= 0 || weekly > 255 || len(times) != int(phaseMax) {
		return BattleConfiguration{}, fmt.Errorf("unsupported raid battle configuration: %s", path)
	}
	config := BattleConfiguration{StartDelaySeconds: uint32(delay), WeeklyClearLimit: int(weekly)}
	seenDisconnectWait := false
	for _, section := range doc.Sections {
		if !strings.EqualFold(strings.TrimSpace(section.Name), "DISCONN MEMBER WAIT TIME") {
			continue
		}
		values := sectionValues(doc, section)
		if seenDisconnectWait || len(values) != 1 || values[0].Kind != dnfpvf.TokenInt || values[0].Int <= 0 || values[0].Int > 3600 {
			return BattleConfiguration{}, fmt.Errorf("invalid raid disconnect wait time: %s", path)
		}
		seenDisconnectWait = true
		config.DisconnectWaitSeconds = uint32(values[0].Int)
	}
	for _, seconds := range times {
		if seconds <= 0 || seconds > math.MaxUint32 {
			return BattleConfiguration{}, fmt.Errorf("invalid raid phase time")
		}
		config.PhaseLimitSeconds = append(config.PhaseLimitSeconds, uint32(seconds))
	}
	inPhase, inDungeon := false, false
	inSingle, singleDungeon, sawSingle := false, false, false
	phase := -1
	var dungeon DungeonState
	hasID, hasState := false, false
	seen := map[uint32]bool{}
	for _, section := range doc.Sections {
		name := strings.ToUpper(strings.TrimSpace(section.Name))
		values := sectionValues(doc, section)
		// The native derived Anton configuration embeds a separate single
		// phase. Its dungeon records must not enter the ordinary phase list.
		if name == "SINGLE RAID PHASE" {
			if inSingle || sawSingle || inPhase || inDungeon {
				return BattleConfiguration{}, fmt.Errorf("nested/repeated single raid phase")
			}
			inSingle, sawSingle = true, true
			continue
		}
		if name == "/SINGLE RAID PHASE" {
			if !inSingle || singleDungeon {
				return BattleConfiguration{}, fmt.Errorf("incomplete single raid phase")
			}
			inSingle = false
			continue
		}
		if inSingle {
			switch name {
			case "PHASE", "/PHASE":
				return BattleConfiguration{}, fmt.Errorf("ordinary raid phase nested in single phase")
			case "DUNGEON INFO":
				if singleDungeon {
					return BattleConfiguration{}, fmt.Errorf("nested single raid dungeon")
				}
				singleDungeon = true
			case "/DUNGEON INFO":
				if !singleDungeon {
					return BattleConfiguration{}, fmt.Errorf("unmatched single raid dungeon end")
				}
				singleDungeon = false
			}
			continue
		}
		switch name {
		case "PHASE":
			if inPhase || inDungeon {
				return BattleConfiguration{}, fmt.Errorf("nested raid phase")
			}
			inPhase = true
			phase++
			seen = map[uint32]bool{}
			config.PhaseDungeons = append(config.PhaseDungeons, nil)
		case "/PHASE":
			if !inPhase || inDungeon || len(config.PhaseDungeons[phase]) == 0 {
				return BattleConfiguration{}, fmt.Errorf("incomplete raid phase")
			}
			inPhase = false
		case "PHASE COIN LIMIT":
			if !inPhase || inDungeon || len(values) != 1 || values[0].Kind != dnfpvf.TokenInt || values[0].Int < 0 || values[0].Int > 255 {
				return BattleConfiguration{}, fmt.Errorf("invalid native raid phase coin limit")
			}
			if config.PhaseCoinLimits == nil {
				config.PhaseCoinLimits = map[byte]uint32{}
			}
			if _, exists := config.PhaseCoinLimits[byte(phase)]; exists {
				return BattleConfiguration{}, fmt.Errorf("duplicate raid phase coin limit")
			}
			config.PhaseCoinLimits[byte(phase)] = uint32(values[0].Int)
		case "DUNGEON INFO":
			if !inPhase || inDungeon {
				return BattleConfiguration{}, fmt.Errorf("raid dungeon outside phase")
			}
			inDungeon = true
			hasID, hasState = false, false
			dungeon = DungeonState{}
		case "INDEX":
			if !inDungeon {
				continue
			}
			if len(values) != 1 || values[0].Kind != dnfpvf.TokenInt || values[0].Int <= 0 || values[0].Int > math.MaxUint32 || hasID {
				return BattleConfiguration{}, fmt.Errorf("invalid raid dungeon index")
			}
			dungeon.ID = uint32(values[0].Int)
			hasID = true
		case "INIT STATE":
			if !inDungeon {
				continue
			}
			if len(values) != 1 || hasState {
				return BattleConfiguration{}, fmt.Errorf("invalid initial raid dungeon state")
			}
			switch strings.ToLower(values[0].Value) {
			case "open":
				dungeon.State = 0
			case "close":
				dungeon.State = 1
			case "hide":
				dungeon.State = 2
			default:
				return BattleConfiguration{}, fmt.Errorf("unsupported initial raid dungeon state %q", values[0].Value)
			}
			hasState = true
		case "/DUNGEON INFO":
			if !inDungeon || !hasID || !hasState || seen[dungeon.ID] {
				return BattleConfiguration{}, fmt.Errorf("incomplete or repeated raid dungeon")
			}
			seen[dungeon.ID] = true
			config.PhaseDungeons[phase] = append(config.PhaseDungeons[phase], dungeon)
			inDungeon = false
		}
	}
	if inSingle || inPhase || inDungeon || len(config.PhaseDungeons) != int(phaseMax) {
		return BattleConfiguration{}, fmt.Errorf("raid phase count mismatch")
	}
	for _, phase := range config.PhaseDungeons {
		if len(phase) > 255 {
			return BattleConfiguration{}, fmt.Errorf("raid phase exceeds native u8 count")
		}
	}
	return config, nil
}

// LoadSingleRaidSequence selects the real link for an authoritative recorded
// weekly clear count. Missing counts/links cannot silently select the first run.
func LoadSingleRaidSequence(source dnfpvf.Source, route byte, weeklyClears int) ([]uint32, error) {
	if route > 1 || weeklyClears < 0 {
		return nil, fmt.Errorf("single raid link context invalid")
	}
	text, err := source.ReadText("etc/raid/singleraidlinksystem.etc")
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse("etc/raid/singleraidlinksystem.etc", text)
	if err != nil {
		return nil, err
	}
	area, count := int64(-1), int64(-1)
	inRaid, inLink := false, false
	var result []uint32
	for _, section := range doc.Sections {
		values := sectionValues(doc, section)
		switch strings.ToLower(strings.TrimSpace(section.Name)) {
		case "raid":
			inRaid = true
			area = -1
		case "/raid":
			inRaid = false
		case "area":
			if inRaid && len(values) == 1 && values[0].Kind == dnfpvf.TokenInt {
				area = values[0].Int
			}
		case "link":
			inLink = true
			count = -1
		case "/link":
			inLink = false
		case "enter count":
			if inLink && len(values) == 1 && values[0].Kind == dnfpvf.TokenInt {
				count = values[0].Int
			}
		case "sequential dungeon index":
			if !inRaid || !inLink || area != int64(route) || count != int64(weeklyClears) {
				continue
			}
			if result != nil || len(values) == 0 || len(values) > 255 {
				return nil, fmt.Errorf("single raid link repeated or empty")
			}
			seen := map[uint32]bool{}
			for _, value := range values {
				if value.Kind != dnfpvf.TokenInt || value.Int <= 0 || value.Int > math.MaxUint32 || seen[uint32(value.Int)] {
					return nil, fmt.Errorf("single raid link invalid dungeon")
				}
				seen[uint32(value.Int)] = true
				result = append(result, uint32(value.Int))
			}
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("single raid link not configured for route=%d clears=%d", route, weeklyClears)
	}
	return result, nil
}

// Current NOTI585 -> 01FEAB30: u8 flag,u8 count,(u32 dungeon,u8 state),u32 infection.
func BuildDungeonStateBody(states []DungeonState, infection uint32) ([]byte, error) {
	if len(states) > 255 {
		return nil, fmt.Errorf("raid dungeon state count exceeds native limit")
	}
	var writer packetWriter
	writer.writeByte(0)
	writer.writeByte(byte(len(states)))
	seen := map[uint32]bool{}
	for _, state := range states {
		if state.ID == 0 || seen[state.ID] {
			return nil, fmt.Errorf("raid dungeon state owner invalid")
		}
		seen[state.ID] = true
		writer.writeUint32(state.ID)
		writer.writeByte(state.State)
	}
	writer.writeUint32(infection)
	return writer.bytes(), nil
}

// Current NOTI596 -> 01FF1380 and NOTI597 -> 01FEABC0.
func BuildTimerBody(kind byte, dungeon, endTimestamp, duration uint32) []byte {
	var writer packetWriter
	writer.writeByte(kind)
	writer.writeUint32(dungeon)
	writer.writeUint32(endTimestamp)
	writer.writeUint32(duration)
	return writer.bytes()
}

func BuildRemainingTimeBody(kind byte, seconds uint32) []byte {
	var writer packetWriter
	writer.writeByte(kind)
	writer.writeUint32(seconds)
	return writer.bytes()
}
