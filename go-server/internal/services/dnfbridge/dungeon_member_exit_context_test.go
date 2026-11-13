package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"testing"
)

func departureRowsInSelectedChannel(t *testing.T, body []byte, channel int) []op9PartyRow {
	t.Helper()
	if len(body) >= 15 && body[9] == 2 {
		return retainedDepartureRows(t, body, channel)
	}
	if int(body[7]) != channel {
		return parseRetirementCurrentRows(t, body)
	}
	if len(body) < 31 || body[9] != 0 {
		t.Fatalf("invalid channel roster: %x", body)
	}
	offset := 16 + int(binary.LittleEndian.Uint32(body[12:16])) + 14
	if offset >= len(body) {
		t.Fatal("truncated actor metadata")
	}
	count := int(body[offset])
	offset++
	if len(body) != offset+count*6+4 {
		t.Fatalf("invalid compact member table: %x", body)
	}
	rows := make([]op9PartyRow, count)
	for i := range rows {
		rows[i] = op9PartyRow{slot: body[offset], uid: binary.LittleEndian.Uint16(body[offset+1:])}
		offset += 6
	}
	return rows
}

func TestDungeonDepartureUpdatesSurvivorTownAndDungeonRecords(t *testing.T) {
	for _, channel := range []int{10, 11, 255, 508} {
		for _, count := range []int{2, 3} {
			for _, leavingID := range []uint16{99, 100} {
				t.Run(fmt.Sprintf("channel%d/members%d/leave%d", channel, count, leavingID), func(t *testing.T) {
					f := newLeaderExitFixture(t)
					f.service.options.currentClientVersion = "official-24.9"
					peers := []*gameSession{f.leader, f.member}
					if count == 3 {
						peers = append(peers, addRetirementPeer(t, f, 101))
					}
					for _, p := range peers {
						p.channel.ID = channel
						p.townActorOwnerChannel = 0
						p.connectionTownActorOwnerChannel = 23
					}
					priorRuntimes := map[*gameSession]any{}
					for _, peer := range peers {
						priorRuntimes[peer] = peer.dungeon.runtime
					}
					leaver := f.member
					if leavingID == 99 {
						leaver = f.leader
					}
					leaver.dungeon.runtime = nil // positive town confirmation already retired only this runtime
					if err := f.service.leaveRuntimePartyOnMemberGiveup(leaver, f.instance); err != nil {
						t.Fatal(err)
					}
					assertDepartureRetiresSelfFirst(t, leaver)
					for _, receiver := range peers {
						if receiver == leaver {
							continue
						}
						state := runtimePartyStateSnapshot(receiver)
						owners := []byte{0}
						if channel <= 255 {
							owners = append(owners, byte(channel))
						}
						rosters, departures := map[byte]int{}, map[byte]int{}
						actorIndex, realtimeIndex, lastRoster := -1, -1, -1
						emptyRealtime := 0
						retired := map[byte]map[uint16]bool{}
						for index, frame := range splitUpperFrames(t, receiver.conn.(*bufferConn).write.Bytes()) {
							if frame.msgID == 9 {
								if len(frame.body) < 10 {
									t.Fatal("truncated retained-party record")
								}
								owner := frame.body[7]
								if owner != 0 && int(owner) != channel {
									t.Fatalf("wrong or truncated channel owner %d", owner)
								}
								id := binary.LittleEndian.Uint16(frame.body[4:6])
								if frame.body[9] == 3 {
									// The paired client retires stale HUD/network bindings
									// only for the last survivor, then restores its fixed slot.
									if count != 2 || lastRoster >= 0 || (id != leavingID && id != receiver.selectedCharacterID) {
										t.Fatal("unexpected party binding retirement")
									}
									if retired[owner] == nil {
										retired[owner] = map[uint16]bool{}
									}
									if retired[owner][id] {
										t.Fatal("duplicate binding retirement")
									}
									retired[owner][id] = true
									continue
								}
								if frame.body[9] == 2 {
									rows := retainedDepartureRows(t, frame.body, channel)
									if id != leavingID || len(rows) != count-1 {
										t.Fatal("wrong departed record")
									}
									for _, row := range rows {
										if row.uid == leavingID {
											t.Fatal("departed actor retained in record")
										}
									}
									departures[owner]++
								} else if frame.body[9] == 0 {
									rows := departureRowsInSelectedChannel(t, frame.body, channel)
									if len(rows) != count-1 {
										t.Fatal("stale member count in record")
									}
									for _, row := range rows {
										if row.uid == leavingID {
											t.Fatal("departed member remains in client record")
										}
										found := false
										for _, member := range state.Members {
											if row.uid == member.UserID && row.slot == member.Slot {
												found = true
											}
										}
										if !found {
											t.Fatal("surviving member identity or fixed slot changed")
										}
									}
									if id == receiver.selectedCharacterID {
										rosters[owner]++
									}
									lastRoster = index
								}
							}
							if frame.msgID == currentTownUserLeaveMsgID {
								actorIndex = index
							}
							if frame.msgID == 153 {
								if len(frame.body) == 0 {
									t.Fatal("truncated realtime roster")
								}
								if count == 2 && frame.body[0] == 0 && lastRoster < 0 {
									emptyRealtime++
									continue // Native singleton binding reset precedes its restored row.
								}
								if frame.body[0] != byte(count-1) {
									t.Fatal("surviving realtime roster lost")
								}
								realtimeIndex = index
							}
						}
						if len(state.Members) != count-1 || any(receiver.dungeon.runtime) != priorRuntimes[receiver] {
							t.Fatal("client binding cleanup retired the server-owned survivor")
						}
						for _, owner := range owners {
							if count == 2 && (!retired[owner][leavingID] || !retired[owner][receiver.selectedCharacterID]) {
								t.Fatal("singleton failed to retire both stale client bindings")
							}
							if rosters[owner] == 0 || departures[owner] != 1 {
								t.Fatalf("receiver=%d owner=%d roster=%d departure=%d; both stored contexts must learn departure", receiver.selectedCharacterID, owner, rosters[owner], departures[owner])
							}
						}
						if count == 2 && emptyRealtime != 1 || count > 2 && emptyRealtime != 0 {
							t.Fatal("unexpected singleton realtime reset count")
						}
						if actorIndex <= lastRoster || realtimeIndex <= actorIndex {
							t.Fatal("active actor removed before both contexts were updated")
						}
					}
				})
			}
		}
	}
}
