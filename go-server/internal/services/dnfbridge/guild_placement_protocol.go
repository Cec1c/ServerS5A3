package dnfbridge

import (
	"encoding/binary"

	"longheng.io/server/internal/modules/dnf/guild"
)

type currentGuildPlacementRequest struct {
	item, slot, x, y, kind, width, height, area uint32
	mask                                        []uint32
}

func parseCurrentGuildPlacementRequest(body []byte) (currentGuildPlacementRequest, error) {
	var q currentGuildPlacementRequest
	if len(body) < 53 {
		return q, guild.ErrPlacementIdentity
	}
	count := binary.LittleEndian.Uint32(body[41:])
	if count == 0 || count > 1024 || len(body) != 53+int(count)*4 {
		return q, guild.ErrPlacementIdentity
	}
	fields := []*uint32{&q.item, &q.slot, &q.x, &q.y, &q.kind, &q.width, &q.height}
	for i, p := range fields {
		*p = binary.LittleEndian.Uint32(body[13+i*4:])
	}
	q.area = binary.LittleEndian.Uint32(body[45:])
	q.mask = make([]uint32, count)
	if q.item == 0 || q.slot > 32767 || q.x > 32767 || q.y > 32767 || q.kind != 0 || q.width < 1 || q.width > 32 || q.height < 1 || q.height > 32 || q.width*q.height != count || q.area > 7 {
		return currentGuildPlacementRequest{}, guild.ErrPlacementIdentity
	}
	for i := range q.mask {
		q.mask[i] = binary.LittleEndian.Uint32(body[53+4*i:])
		if q.mask[i] > 1 {
			return currentGuildPlacementRequest{}, guild.ErrPlacementIdentity
		}
	}
	return q, nil
}

func currentGuildPlacementRecord(p guild.Placement) []byte {
	b := make([]byte, 23)
	binary.LittleEndian.PutUint32(b, p.ID)
	binary.LittleEndian.PutUint32(b[4:], p.ObjectID)
	binary.LittleEndian.PutUint32(b[8:], p.CreatorID)
	b[12] = p.Area
	binary.LittleEndian.PutUint16(b[15:], uint16(p.X))
	binary.LittleEndian.PutUint16(b[17:], uint16(p.Y))
	binary.LittleEndian.PutUint32(b[19:], uint32(p.ReadyAt.Unix()))
	return b
}

func currentGuildPlacementCosts(g *guild.Guild) [8]uint32 {
	var costs [8]uint32
	if g != nil && g.Placements != nil {
		for _, p := range g.Placements.Rows {
			if p.Area < 8 {
				costs[p.Area] += p.Maintenance
			}
		}
	}
	return costs
}

func buildCurrentGuildPlacementSnapshot(g *guild.Guild) ([]byte, error) {
	if g != nil {
		if err := g.Placements.Validate(); err != nil {
			return nil, err
		}
	}
	tail, err := buildCurrentGuildGuardianSnapshot(g)
	if err != nil {
		return nil, err
	}
	body := make([]byte, 1+0x369a)
	body[0] = 1
	copy(body[1+0x2e05:], tail)
	var slots [8]int
	if g != nil && g.Placements != nil {
		for _, p := range g.Placements.Rows {
			at := 1 + 5 + (int(p.Area)*64+slots[p.Area])*23
			copy(body[at:], currentGuildPlacementRecord(p))
			slots[p.Area]++
		}
	}
	return body, nil
}

func buildCurrentGuildPlacementNotice(action uint32, p guild.Placement, cost uint32) []byte {
	b := make([]byte, 39)
	binary.LittleEndian.PutUint32(b, action)
	binary.LittleEndian.PutUint32(b[4:], cost)
	copy(b[16:], currentGuildPlacementRecord(p))
	return b
}
