package raid

import (
	"fmt"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"strings"
)

// LukeSanctumMazeConfiguration preserves the actual CARD SET MAP INFO rows.
// A row belongs to an admitted battle; this resource supplies no daily policy.
type LukeSanctumMazeConfiguration struct {
	Dungeons [3]uint32
	Records  [][3]uint32
}

func (c LukeSanctumMazeConfiguration) Clone() LukeSanctumMazeConfiguration {
	c.Records = append([][3]uint32(nil), c.Records...)
	return c
}

func LoadLukeSanctumMazeConfiguration(source dnfpvf.Source) (LukeSanctumMazeConfiguration, error) {
	const path = "etc/raid/luke/luke.etc"
	text, err := source.ReadText(path)
	if err != nil {
		return LukeSanctumMazeConfiguration{}, err
	}
	doc, err := dnfpvf.Parse(path, text)
	if err != nil {
		return LukeSanctumMazeConfiguration{}, err
	}
	var c LukeSanctumMazeConfiguration
	var values []int64
	seen := map[string]bool{}
	for _, section := range doc.Sections {
		name := strings.ToUpper(strings.TrimSpace(section.Name))
		component := -1
		switch name {
		case "CARD SET MAP INFO":
			if seen[name] {
				return c, fmt.Errorf("repeated Luke card set map info")
			}
			seen[name] = true
			for _, token := range sectionValues(doc, section) {
				if token.Kind != dnfpvf.TokenInt {
					return c, fmt.Errorf("noninteger Luke card set map info")
				}
				values = append(values, token.Int)
			}
		case "BIRTH SANCTUM":
			component = 0
		case "EXTINCTION SANCTUM":
			component = 1
		case "SPREAD SANCTUM":
			component = 2
		}
		if component >= 0 {
			tokens := sectionValues(doc, section)
			if seen[name] || len(tokens) != 2 || tokens[0].Kind != dnfpvf.TokenInt || tokens[1].Kind != dnfpvf.TokenInt || tokens[0].Int <= 0 || tokens[1].Int <= 0 || tokens[1].Int > 0xffffffff {
				return c, fmt.Errorf("invalid Luke sanctum resource owner")
			}
			seen[name] = true
			c.Dungeons[component] = uint32(tokens[1].Int)
		}
	}
	if len(values) == 0 || len(values)%3 != 0 || len(values)/3 > 127 || c.Dungeons != [3]uint32{5054, 5055, 5056} {
		return c, fmt.Errorf("unsupported Luke sanctum card set contract")
	}
	for index := 0; index < len(values); index += 3 {
		var row [3]uint32
		for component := range row {
			value := values[index+component]
			if value < 0 || value > 0xffffffff-2 {
				return c, fmt.Errorf("Luke maze group start out of range")
			}
			row[component] = uint32(value)
			for _, old := range c.Records {
				a, b := old[component], row[component]
				if a <= b+2 && b <= a+2 {
					return c, fmt.Errorf("overlapping Luke maze triplets")
				}
			}
		}
		c.Records = append(c.Records, row)
	}
	return c, nil
}

func (c LukeSanctumMazeConfiguration) Component(dungeon uint32) (int, bool) {
	for index, id := range c.Dungeons {
		if id == dungeon {
			return index, true
		}
	}
	return 0, false
}

func (c LukeSanctumMazeConfiguration) RecordForMaze(dungeon uint32, maze, mazeCount int) (int, error) {
	component, ok := c.Component(dungeon)
	if !ok || maze < 0 || maze >= mazeCount {
		return 0, fmt.Errorf("Luke maze selection has no resource owner")
	}
	selected := -1
	for index, row := range c.Records {
		start := int(row[component])
		if start < 0 || start+2 >= mazeCount {
			return 0, fmt.Errorf("Luke maze triplet exceeds actual dungeon")
		}
		if maze >= start && maze <= start+2 {
			if selected >= 0 {
				return 0, fmt.Errorf("Luke maze belongs to multiple triplets")
			}
			selected = index
		}
	}
	if selected < 0 {
		return 0, fmt.Errorf("Luke maze belongs to no triplet")
	}
	return selected, nil
}
