package dnfbridge

import dnfguild "longheng.io/server/internal/modules/dnf/guild"

type currentGuildSecedeRequest struct {
	Region byte
	Name   string
}

// The two native senders use the actor's region or a named target's region.
// The representative-selection UI also emits region0 with an empty name.
// Identity matching belongs to the transaction, after resolving an owned
// character; a nonempty name must not be treated as a self-exit shortcut.
func parseCurrentGuildSecedeRequest(body []byte) (currentGuildSecedeRequest, bool) {
	if len(body) < 5 || body[0] >= 250 {
		return currentGuildSecedeRequest{}, false
	}
	name, ok := parseCurrentGuildText(body[1:], rosterNameMaxBytes)
	if !ok {
		return currentGuildSecedeRequest{}, false
	}
	return currentGuildSecedeRequest{Region: body[0], Name: name}, true
}

// class1/op153 consumes the status in the dispatcher. Its callback then reads
// action, guild-name DSTR, one unused byte, character-name DSTR, in that order.
// This is not the unrelated class0/op153 counted-character notification.
func buildCurrentGuildSecedeSuccessBody(result dnfguild.MemberRemoval) []byte {
	var w packetWriter
	action := byte(2)
	if result.Voluntary {
		action = 1
	}
	w.writeByte(action)
	writeClientNameDstr(&w, result.GuildName)
	w.writeByte(0)
	writeClientNameDstr(&w, result.Member.Name)
	return w.bytes()
}
