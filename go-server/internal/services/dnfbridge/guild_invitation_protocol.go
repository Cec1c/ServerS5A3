package dnfbridge

import (
	"errors"
	"strings"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
)

const (
	currentGuildInviteOpcode       = uint16(151)
	currentGuildInviteReplyOpcode  = uint16(152)
	currentGuildInviteNotification = uint16(147)
	currentGuildInviteResult       = uint16(148)
)

func parseCurrentGuildInvitation(body []byte) (byte, string, bool) {
	if len(body) < 6 || body[0] >= 250 {
		return 0, "", false
	}
	name, ok := parseCurrentGuildText(body[1:], rosterNameMaxBytes)
	if !ok || name == "" || strings.TrimSpace(name) != name {
		return 0, "", false
	}
	return body[0], name, true
}

// v105 class0/147: four DSTRs are interspersed with numeric fields. In
// particular inviter name is the SECOND string; the first is the guild name.
func buildCurrentGuildInvitationBody(g *dnfguild.Guild, inviter string) ([]byte, error) {
	if g == nil || g.ID == 0 || g.MemberLimit <= 0 || uint64(g.MemberLimit) > 0xffffffff || len(g.Members) > 0xffff || len(g.Tags) > 0xff {
		return nil, dnfguild.ErrInvalidRegistry
	}
	for _, field := range []struct {
		value string
		limit int
	}{{g.Name, 0x32}, {inviter, 0x80}, {g.Promotion, 0x100}, {g.MasterName, 0x30}} {
		encoded := guildInvitationString(field.value)
		if _, ok := parseCurrentGuildText(encoded, field.limit); !ok {
			return nil, dnfguild.ErrInvalidRegistry
		}
	}
	var w packetWriter
	writeClientNameDstr(&w, g.Name)
	writeClientNameDstr(&w, inviter)
	writeClientNameDstr(&w, g.Promotion)
	w.writeUint32(uint32(g.Level))
	w.writeUint16(uint16(len(g.Members)))
	w.writeByte(byte(len(g.Tags)))
	for _, tag := range g.Tags {
		w.writeByte(tag)
	}
	w.writeUint32(g.ID)
	writeClientNameDstr(&w, g.MasterName)
	w.writeUint32(uint32(g.MemberLimit))
	return w.bytes(), nil
}

func guildInvitationString(value string) []byte {
	var w packetWriter
	writeClientNameDstr(&w, value)
	return w.bytes()
}

func buildCurrentGuildInvitationResult(result byte, name string) []byte {
	var w packetWriter
	w.writeByte(result)
	if result < 2 {
		writeClientNameDstr(&w, name)
	}
	return w.bytes()
}

func currentGuildInvitationError(err error) byte {
	switch {
	case errors.Is(err, errCurrentGuildInvitationChannel):
		return 3
	case errors.Is(err, errCurrentGuildInvitationExpired):
		return 103
	case errors.Is(err, dnfguild.ErrPermissionDenied):
		return 36
	case errors.Is(err, dnfguild.ErrAlreadyMember):
		return 39
	case errors.Is(err, dnfguild.ErrMemberLimit):
		return 38
	case errors.Is(err, dnfguild.ErrNotMember), errors.Is(err, dnfguild.ErrGuildNotFound):
		return 34
	default:
		return 23
	}
}
