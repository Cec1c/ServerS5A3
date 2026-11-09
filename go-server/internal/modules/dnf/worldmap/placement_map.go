package worldmap

import (
	"fmt"
	"path"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// LoadTownPlacementMap resolves a town's explicit map path, including imported
// NPC spawns. Incomplete collision/NPC data must not authorize a store position.
func LoadTownPlacementMap(source dnfpvf.Source, mapPath string) (Map, error) {
	if source == nil {
		return Map{}, ErrSourceRequired
	}
	load := func(reference string) (string, *dnfpvf.Document, error) {
		resolved := resolveSourceReference(DefaultMapList, reference)
		lower := strings.ToLower(resolved)
		candidates := []string{resolved}
		if lower != resolved {
			candidates = append(candidates, lower)
		}
		if base := path.Base(lower); base != "." && base != "/" && !strings.HasPrefix(base, "(r)") {
			// Town indexes retain logical names when only the revised file
			// exists. Match the established NPC/drop same-directory fallback.
			candidates = append(candidates, path.Join(path.Dir(lower), "(r)"+base))
		}
		var text string
		var readErr error
		for _, candidate := range candidates {
			text, readErr = source.ReadText(candidate)
			if readErr == nil {
				resolved = candidate
				break
			}
		}
		if readErr != nil {
			return "", nil, fmt.Errorf("read dnf town placement map %q: %w", reference, readErr)
		}
		// A readable original with invalid data must fail, even if a revised
		// file also exists; fallback cannot conceal incomplete placement.
		doc, err := ParseDocument(resolved, text)
		if err != nil {
			return "", nil, err
		}
		parsed := ParseMap(0, resolved, doc)
		for _, d := range parsed.Diagnostics {
			if sectionKey(d.Section) == "npc" || sectionKey(d.Section) == "virtual movable area" {
				return "", nil, fmt.Errorf("invalid placement data in %s: %s", resolved, d.Message)
			}
		}
		return resolved, doc, nil
	}
	resolved, doc, err := load(mapPath)
	if err != nil {
		return Map{}, err
	}
	return parseMapWithImports(0, resolved, doc, load)
}
