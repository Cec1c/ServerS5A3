package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func newInspectedPeerFixture(t *testing.T) *townPartyActorHPFixture {
	t.Helper()
	f := newTownPartyActorHPFixture(t, false)
	f.target.accountID, f.actor.accountID = "dnf:1", "dnf:2"
	f.target.townActorOwnerChannel = byte(f.target.channel.ID)
	f.target.connectionTownActorOwnerChannel = byte(f.target.channel.ID)
	storeRuntimePartyState(f.target, alignedcmd.PartyState{})
	storeRuntimePartyState(f.actor, alignedcmd.PartyState{})
	return f
}

func inspectFixturePeer(t *testing.T, f *townPartyActorHPFixture) {
	t.Helper()
	handled, err := f.service.handleCurrentPeerUserInfoRequest(f.target, []byte{17, 0, 3})
	if err != nil || !handled {
		t.Fatalf("inspect peer: handled=%v err=%v", handled, err)
	}
	if sub := f.target.peerProfileSubscription; sub.actor.session != f.actor || sub.actor.character != 17 || sub.viewer.character != 29 {
		t.Fatalf("successful inspection did not retain exact pair: %+v", sub)
	}
	f.reset()
}

func TestCurrentInspectedProfileRefreshesUnrelatedSameAreaPeer(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "off")
	t.Setenv(peerSkillPageEnv, "off")
	f := newInspectedPeerFixture(t)
	f.service.broadcastCurrentCrossSceneAppearance(f.actor)
	if f.targetConn.write.Len() != 0 {
		t.Fatal("unsolicited stranger profile sent before inspection")
	}
	inspectFixturePeer(t, f)
	repos, _ := f.service.repositoryGroup()
	ctx := context.Background()
	for _, level := range []int{8, 11, 0} {
		if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "17", Skills: map[int64]dnfrepo.SkillState{99: {Level: level}}}); err != nil {
			t.Fatal(err)
		}
		f.reset()
		f.service.broadcastCurrentCrossSceneAppearance(f.actor)
		body := requireCurrentCrossSceneProfileBatchMode8(t, splitUpperFrames(t, f.targetConn.write.Bytes()), 17, currentTownRemoteActorOwnerContext(f.target, 17))
		_, name, character, found := f.service.characterForEnter(ctx, f.actor, 17)
		if !found {
			t.Fatal("missing actor")
		}
		mode0, err := f.service.buildCurrentSceneObjectListBodyForSessionInContextStrict(ctx, f.actor, 17, name, character, true, currentTownRemoteActorOwnerContext(f.target, 17))
		if err != nil {
			t.Fatal(err)
		}
		tail := body[len(mode0):]
		offset := crossSceneBuffMode3TailSkillPageOffset
		want := []byte{255, 1, 99, 0, byte(level), 1, 99, 0, byte(level)}
		if level == 0 {
			want = []byte{255, 0, 0}
		}
		if !bytes.Equal(tail[offset:offset+len(want)], want) {
			t.Fatalf("fresh learned level=%d page=%x want=%x", level, tail[offset:offset+len(want)], want)
		}
		if f.actorConn.write.Len() != 0 {
			t.Fatal("refresh wrote to actor")
		}
	}
}

func TestCurrentInspectedProfileRejectsStalePreparedState(t *testing.T) {
	for _, phase := range []string{"before_prepare", "during_repository_load"} {
		for _, change := range []string{"leave_area", "viewer_channel", "actor_channel", "viewer_account", "actor_account", "viewer_generation", "actor_generation", "new_inspection", "appearance_revision", "route"} {
			t.Run(phase+"/"+change, func(t *testing.T) {
				f := newInspectedPeerFixture(t)
				inspectFixturePeer(t, f)
				sub := f.target.peerProfileSubscription
				mutate := func() {
					switch change {
					case "leave_area":
						f.service.onlinePlayers.LeaveArea(17)
					case "viewer_channel":
						f.target.channel.ID++
						f.target.residentChannel.ID++
					case "actor_channel":
						f.actor.channel.ID++
						f.actor.residentChannel.ID++
					case "viewer_account":
						f.target.accountID = "other"
					case "actor_account":
						f.actor.accountID = "other"
					case "viewer_generation":
						f.target.characterGeneration++
					case "actor_generation":
						f.actor.characterGeneration++
					case "new_inspection":
						f.target.peerProfileSubscription = currentPeerProfileSubscription{}
					case "appearance_revision":
						f.actor.crossSceneAppearanceRevision++
					case "route":
						f.service.options.channelAdvertiseID++
					}
				}
				if phase == "before_prepare" {
					mutate()
				} else {
					changed := false
					wrapCurrentCrossSceneProfileBatchCharacterLoads(t, f.service, func(string) {
						if !changed {
							changed = true
							mutate()
						}
					})
				}
				if err := f.service.sendCurrentInspectedProfile(context.Background(), sub); err != nil {
					t.Fatal(err)
				}
				// A revision that changed before preparation is the new valid state.
				if phase == "before_prepare" && change == "appearance_revision" {
					requireCurrentCrossSceneProfileBatchMode8(t, splitUpperFrames(t, f.targetConn.write.Bytes()), 17, sub.owner)
				} else if f.targetConn.write.Len() != 0 {
					t.Fatalf("stale %s profile emitted", change)
				}
			})
		}
	}
}

func TestCurrentInspectedProfileBindingCleanup(t *testing.T) {
	for _, side := range []string{"viewer", "actor"} {
		for _, action := range []string{"same_binding", "switch", "disconnect"} {
			t.Run(side+"/"+action, func(t *testing.T) {
				f := newInspectedPeerFixture(t)
				inspectFixturePeer(t, f)
				session := f.target
				if side == "actor" {
					session = f.actor
				}
				old := f.target.peerProfileSubscription
				switch action {
				case "same_binding":
					f.service.bindGameSessionCharacter(session, session.selectedCharacterID)
				case "switch":
					f.service.bindGameSessionCharacter(session, 77)
				case "disconnect":
					f.service.unbindGameSession(session)
				}
				if action == "same_binding" {
					if f.target.peerProfileSubscription != old {
						t.Fatal("unchanged binding cleared open inspection")
					}
				} else {
					if f.target.peerProfileSubscription.actor.session != nil {
						t.Fatal("retired binding retained subscription")
					}
					f.service.rememberCurrentPeerProfile(old.viewer, old.actor, old.owner)
					if f.target.peerProfileSubscription.actor.session != nil {
						t.Fatal("stale successful response resurrected subscription")
					}
				}
			})
		}
	}
}

func TestCurrentInspectedProfilePartyRefreshNotDuplicated(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	inspectFixturePeer(t, &townPartyActorHPFixture{service: f.service, target: f.target, actor: f.actor, targetConn: f.targetConn, actorConn: f.actorConn})
	f.service.broadcastCurrentCrossSceneAppearance(f.actor)
	requireCurrentCrossSceneProfileBatchMode8(t, splitUpperFrames(t, f.targetConn.write.Bytes()), 17, currentConnectionTownActorOwnerContext(f.target))
}
