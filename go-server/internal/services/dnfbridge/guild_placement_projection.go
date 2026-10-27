package dnfbridge

import (
	"bytes"
	"slices"
	"sync"

	"longheng.io/server/internal/modules/dnf/guild"
)

type currentGuildPlacementProjection struct {
	mu             sync.Mutex
	frozenPractice boundGameSessionCharacter
	identity       boundGameSessionCharacter
	guildID        uint32
	rows           map[uint32]guild.Placement
}

// A full 191 initializes a reconnected client's placement table. Subsequent
// income and member refreshes send only changed rows, preserving the scene.
// Caller serializes this projection with its adjacent 1297 snapshot.
func (s *Service) writeCurrentGuildPlacementProjectionLocked(session *gameSession, g *guild.Guild, identity boundGameSessionCharacter) error {
	p := &session.guild.placement
	if g == nil {
		if len(p.rows) > 0 {
			body, err := buildCurrentGuildPlacementSnapshot(nil)
			if err != nil {
				return err
			}
			if err = s.sendGameUpperRawClass(session, 191, body, 0); err != nil {
				return err
			}
		}
		p.identity = boundGameSessionCharacter{}
		p.guildID = 0
		p.rows = nil
		return nil
	}
	if err := g.Placements.Validate(); err != nil {
		return err
	}
	rows := map[uint32]guild.Placement{}
	if g.Placements != nil {
		for _, row := range g.Placements.Rows {
			rows[row.ID] = row
		}
	}
	if p.identity != identity || p.guildID != g.ID || p.rows == nil {
		// The empty constructor already represents a guild without placements.
		if len(rows) > 0 || len(p.rows) > 0 {
			body, err := buildCurrentGuildPlacementSnapshot(g)
			if err != nil {
				return err
			}
			if err = s.sendGameUpperRawClass(session, 191, body, 0); err != nil {
				return err
			}
		}
		p.identity = identity
		p.guildID = g.ID
		p.rows = rows
		return nil
	}
	costs := currentGuildPlacementCosts(g)
	ids := make([]uint32, 0, len(p.rows)+len(rows))
	seen := map[uint32]bool{}
	for id := range p.rows {
		ids = append(ids, id)
		seen[id] = true
	}
	for id := range rows {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		old, had := p.rows[id]
		next, has := rows[id]
		if had && has && bytes.Equal(currentGuildPlacementRecord(old), currentGuildPlacementRecord(next)) {
			continue
		}
		action := uint32(4)
		row := next
		if !has {
			action = 2
			row = old
		}
		if err := s.sendGameUpperRawClass(session, 777, buildCurrentGuildPlacementNotice(action, row, costs[row.Area]), 0); err != nil {
			return err
		}
		if has {
			p.rows[id] = next
		} else {
			delete(p.rows, id)
		}
	}
	return nil
}
