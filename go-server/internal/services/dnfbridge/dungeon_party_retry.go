package dnfbridge

import (
	"context"
	"fmt"
)

// A settlement retry must retire every member before the ordinary town entry
// preflight runs. Retiring only the leader leaves the peer in the previous
// dungeon and makes the same-area check reject the entire new run.
func (s *Service) retireCompletedPartyForRetry(leader *gameSession, runtime *runtimeDungeonState, owners ...[]currentRaidRetryReturnOwner) (bool, error) {
	return s.retireCompletedPartyForAction(leader, runtime, true, owners...)
}

func (s *Service) retireCompletedPartyForAction(leader *gameSession, runtime *runtimeDungeonState, requireOpenRaidMap bool, ownerSets ...[]currentRaidRetryReturnOwner) (bool, error) {
	var owners []currentRaidRetryReturnOwner
	if len(ownerSets) > 0 {
		owners = ownerSets[0]
	}
	state := runtimePartyStateSnapshot(leader)
	members := runtimePartyMembers(state)
	if state.PartyID <= 0 || len(members) < 2 {
		retired, err := s.retireCompletedDungeonForTownAction(leader, requireOpenRaidMap, runtime)
		if retired {
			if owner := currentRaidRetryOwnerForSession(owners, leader); owner != nil && owner.runtime == runtime {
				owner.retired = true
			}
		}
		return retired, err
	}
	if state.UserID != leader.selectedCharacterID || runtime.partyInstance == nil {
		return false, fmt.Errorf("party retry requires the completed instance leader")
	}
	type memberRun struct {
		session *gameSession
		runtime *runtimeDungeonState
		origin  currentTownPositionSnapshot
	}
	runs := make([]memberRun, 0, len(members))
	// Validate the whole group before detaching any old runtime or publishing
	// temporary entry presence. Rewards remain the existing per-member owner.
	for _, member := range members {
		peer, online := s.onlineGameSession(member.UserID)
		if !online || peer == nil {
			return false, fmt.Errorf("party retry member %d is offline", member.UserID)
		}
		peerState := runtimePartyStateSnapshot(peer)
		peer.dungeon.mu.Lock()
		old := peer.dungeon.runtime
		valid := peerState.PartyID == state.PartyID && peerState.UserID == state.UserID &&
			old != nil && old.partyInstance == runtime.partyInstance &&
			s.currentCompletedDungeonTownActionReady(peer, old, requireOpenRaidMap) && (!requireOpenRaidMap || old.raidEntry == nil || s.currentRaidDungeonSelectionAllowed(peer, uint32(old.Dungeon.ID)))
		if owner := currentRaidRetryOwnerForSession(owners, peer); len(owners) > 0 && (owner == nil || owner.runtime != old) {
			valid = false
		}
		var origin currentTownPositionSnapshot
		if valid {
			origin = old.townReturnOrigin
			valid = origin.CharacterID == member.UserID && origin.PositionValid &&
				origin.TownID == runtime.townReturnOrigin.TownID && origin.AreaID == runtime.townReturnOrigin.AreaID &&
				currentTownPresenceChannelID(peer) == currentTownPresenceChannelID(leader)
		}
		peer.dungeon.mu.Unlock()
		if !valid {
			return false, fmt.Errorf("party retry member %d has not completed the same settlement", member.UserID)
		}
		runs = append(runs, memberRun{peer, old, origin})
	}
	// Keep the leader's completed runtime available until every peer has
	// retired, so a peer failure cannot remove the leader's return-town owner.
	for i := range runs {
		if runs[i].session == leader {
			runs[i], runs[len(runs)-1] = runs[len(runs)-1], runs[i]
			break
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	for _, run := range runs {
		retire := func() error {
			retired, err := s.retireCompletedDungeonForTownAction(run.session, requireOpenRaidMap, run.runtime)
			if err != nil {
				return err
			}
			if !retired {
				return fmt.Errorf("party retry member %d retirement declined", run.session.selectedCharacterID)
			}
			if owner := currentRaidRetryOwnerForSession(owners, run.session); owner != nil && owner.runtime == run.runtime {
				owner.retired = true
			}
			if _, bound := bindCurrentTownSelectorOrigin(run.session); !bound {
				return fmt.Errorf("party retry member %d origin unavailable", run.session.selectedCharacterID)
			}
			// This presence is only the entry preflight's frozen origin. Do not
			// send town appearance/placement while the client shows settlement.
			if s.onlinePlayers != nil {
				character := run.runtime.Character
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{
					CharacterID: run.session.selectedCharacterID, AccountID: character.AccountID,
					Name: character.Name, Job: byte(numericCharacterStat(character.Job)),
					GrowType: characterGrowTypeByte(character), Level: byte(character.Level),
					TownID: run.origin.TownID, AreaID: run.origin.AreaID,
					ChannelID: currentTownPresenceChannelID(run.session), Session: run.session,
					PositionX: run.origin.PositionX, PositionY: run.origin.PositionY, Direction: run.origin.MovementCode,
				})
			}
			s.forgetStaleDungeonPartyRunKeyOutsideDungeon(run.session, "completed_party_retry")
			return nil
		}
		var err error
		if run.session == leader {
			err = retire()
		} else {
			err = s.callGameSession(ctx, run.session, "completed-party-retry-retire", retire)
		}
		if err != nil {
			return false, err
		}
	}
	s.logGameEvent(leader, "game-dungeon-party-retry-retired", "party_id", state.PartyID,
		"member_count", len(runs), "dungeon_id", runtime.Dungeon.ID,
		"reason", "every_completed_member_is_ready_for_the_shared_entry_preflight")
	return true, nil
}
