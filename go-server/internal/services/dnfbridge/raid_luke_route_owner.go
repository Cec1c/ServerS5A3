package dnfbridge

import (
	"fmt"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

type currentLukeMemberRoute struct {
	Attempt uint64
	Route   uint32
}

// The resource's LOCKOUT TYPE 4/5 owns light/dark membership. Zero is
// shared energy control/storage and must never choose or change a route.
func loadCurrentLukeLockouts(source dnfpvf.Source) (map[uint32]uint32, error) {
	text, err := source.ReadText("etc/raid/luke/luke.etc")
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse("etc/raid/luke/luke.etc", text)
	if err != nil {
		return nil, err
	}
	routes := map[uint32]uint32{}
	phase, dungeon := -1, uint32(0)
	inDungeon := false
	for _, section := range doc.Sections {
		values := doc.Tokens[section.Start:section.End]
		switch strings.ToUpper(strings.TrimSpace(section.Name)) {
		case "PHASE":
			phase++
		case "DUNGEON INFO":
			inDungeon, dungeon = true, 0
		case "/DUNGEON INFO":
			if phase == 1 {
				if _, ok := routes[dungeon]; !ok {
					return nil, fmt.Errorf("Luke dungeon lockout missing")
				}
			}
			inDungeon = false
		case "INDEX":
			if inDungeon && len(values) == 1 && values[0].Kind == dnfpvf.TokenInt && values[0].Int > 0 && values[0].Int <= int64(^uint32(0)) {
				dungeon = uint32(values[0].Int)
			}
		case "LOCKOUT TYPE":
			if !inDungeon || phase != 1 {
				continue
			}
			if dungeon == 0 || len(values) != 2 || values[0].Kind != dnfpvf.TokenInt || values[1].Kind != dnfpvf.TokenInt || values[1].Int != 0 || (values[0].Int != 0 && values[0].Int != 4 && values[0].Int != 5) {
				return nil, fmt.Errorf("Luke dungeon lockout invalid")
			}
			if _, ok := routes[dungeon]; ok {
				return nil, fmt.Errorf("Luke dungeon lockout repeated")
			}
			routes[dungeon] = uint32(values[0].Int)
		}
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("Luke phase two lockouts unavailable")
	}
	return routes, nil
}

// Caller holds raidMu. This is checked for every prepared follower as well
// as the selecting owner, before entry costs or runtime commits.
func (s *Service) currentLukeRouteAllowedLocked(b *runtimeRaidBattle, character uint16, dungeon uint32) bool {
	if b == nil || b.Mode != 2 || b.Single || b.Phase != 1 {
		return true
	}
	if b.Anton == nil || !b.Anton.IsLuke() {
		return false
	}
	s.initialEquipmentMu.Lock()
	source, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return false
	}
	routes, err := loadCurrentLukeLockouts(source)
	if err != nil {
		return false
	}
	route, ok := routes[dungeon]
	if !ok {
		return false
	}
	chosen := b.LukeMemberRoutes[character]
	return route == 0 || chosen.Attempt != b.Anton.Epoch() || chosen.Route == 0 || chosen.Route == route
}

// Only accepted scene entry chooses a route; preparation and failed entry
// never do. The battle journal survives socket replacement and roster moves.
func (s *Service) commitCurrentLukeRouteLocked(b *runtimeRaidBattle, character uint16, dungeon uint32) error {
	if b == nil || b.Mode != 2 || b.Single || b.Phase != 1 {
		return nil
	}
	if !s.currentLukeRouteAllowedLocked(b, character, dungeon) {
		return fmt.Errorf("Luke character belongs to the other route")
	}
	s.initialEquipmentMu.Lock()
	source, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return err
	}
	routes, err := loadCurrentLukeLockouts(source)
	if err != nil {
		return err
	}
	if route := routes[dungeon]; route != 0 {
		if b.LukeMemberRoutes == nil {
			b.LukeMemberRoutes = map[uint16]currentLukeMemberRoute{}
		}
		b.LukeMemberRoutes[character] = currentLukeMemberRoute{Attempt: b.Anton.Epoch(), Route: route}
	}
	return nil
}
