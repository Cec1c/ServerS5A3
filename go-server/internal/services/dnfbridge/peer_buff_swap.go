package dnfbridge

import (
	"context"
	"strconv"
)

type nativePeerBuffInspectKey struct{}

// Inspection needs all learned levels, including weapon masteries. The
// separate Buff carrier still serializes only its selected registration.
type nativePeerProfileSkillsKey struct{}

// Only the selected Buff skill is needed to render its learned base level.
// Do not include the target's other learned skills or account metadata.
func (r *csharpLegacyUserInfoReader) writeNativePeerBuffSkillPage(w *packetWriter) {
	end := r.beginNativeBuffOwnedSerialization()
	defer end()
	var entries []peerSkillPageEntry
	if projection, err := r.loadNativeBuffOwnedProjection(); err == nil {
		state := projection.State
		if state.Skill != 0 {
			for _, entry := range peerSkillPageEntries(r.loadPeerSkillPageSkills(r.characterID)) {
				if entry.SkillID == state.Skill {
					entries = append(entries, entry)
					break
				}
			}
		}
	}
	writePeerSkillPageBlockEntries(w, peerSkillPageSelectedResourceGroup, entries)
}

// The native client has no actor-addressed standalone Buff notification.
// Use the required mode1 envelope but carry only registrations and selected
// Buff skill. Reuse the exact public actor stat prefix already sent in mode3;
// no account/honor/other bag rows, worn equipment, pet or adventure data.
// Mode3 follows immediately to restore the public worn-actor create state.
func (s *Service) buildNativePeerBuffCarrier(ctx context.Context, target *gameSession, targetID uint16, detail []byte) []byte {
	const mode3PublicStateStart = 15
	const mode3PublicStateEnd = mode3PublicStateStart + 8 + currentMode1StatBlobWireSize + 1
	if len(detail) < mode3PublicStateEnd || detail[0] != 3 {
		return nil
	}
	r := csharpLegacyUserInfoReader{ctx: ctx, service: s, session: target, characterID: strconv.Itoa(int(targetID))}
	end := r.beginNativeBuffOwnedSerialization()
	defer end()
	var w packetWriter
	w.writeByte(1)
	w.writeUint16(1)
	w.writeBytes(detail[3:5])      // existing target route/owner
	w.writeBytes(make([]byte, 16)) // required auxiliary envelope, no account state
	w.writeUint16(currentSceneActorObjectKey(targetID))
	w.writeBytes(detail[mode3PublicStateStart:mode3PublicStateEnd])
	w.writeByte(0) // no additional worn equipment create rows
	w.writeUint32(0)
	r.writeNativeBuffSwapUserInfo(&w)
	w.writeUint32(0) // no additional pet state
	w.writeUint32(0)
	r.writeNativePeerBuffSkillPage(&w)
	w.writeBytes(make([]byte, 20)) // required empty auxiliary tail
	r.writePeerBuffViewText(&w, targetID)
	return w.bytes()
}
