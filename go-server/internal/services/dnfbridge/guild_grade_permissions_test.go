package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func guildGradePermissionsBody(grade byte, mask uint32) []byte {
	body := make([]byte, 5)
	body[0] = grade
	binary.LittleEndian.PutUint32(body[1:], mask)
	return body
}

func TestGuildGradePermissionsNativeFlow(t *testing.T) {
	s, peers, repos := guildTextFixture(t)
	ctx := context.Background()
	guildDispatch(t, s, peers[0], 748, guildGradePermissionsBody(4, 2|16))
	if got := guildReply(t, peers[0], 748); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("ACK=%x", got)
	}
	r, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil || r.Guilds[1].GradePermissions[4] != 18 || r.Guilds[1].Members["2"].Grade != 4 {
		t.Fatalf("permission update changed role: %+v %v", r, err)
	}
	// Consume the fixed NOTI70 prefix independently to locate six masks. The
	// grade masks follow the promotion DSTR, each followed by 24 name bytes.
	for _, peer := range peers[:2] {
		body := guildReply(t, peer, 70)
		skip := func() {
			t.Helper()
			if len(body) < 4 {
				t.Fatal("short DSTR")
			}
			n := int(binary.LittleEndian.Uint32(body))
			if n > len(body)-4 {
				t.Fatal("bad DSTR length")
			}
			body = body[4+n:]
		}
		skip()
		body = body[5:]
		skip()
		body = body[8:]
		skip()
		skip()
		body = body[2:]
		skip()
		if len(body) < 6*28 || binary.LittleEndian.Uint32(body[4*28:]) != 18 {
			t.Fatal("native role mask projection missing")
		}
	}
	if peers[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("permission update crossed guilds")
	}
	guildDispatch(t, s, peers[1], 154, guildWireString("获授权公告"))
	if !bytes.Equal(guildReply(t, peers[1], 154), []byte{1}) {
		t.Fatal("new notice permission did not take effect")
	}
	guildDispatch(t, s, peers[1], 740, guildSettingsBody(2, "stale", 0, 0, 0))
	if !bytes.Equal(guildReply(t, peers[1], 740), []byte{1}) {
		t.Fatal("new settings permission did not take effect")
	}
	guildDispatch(t, s, peers[0], 748, guildGradePermissionsBody(4, 0))
	guildDispatch(t, s, peers[1], 154, guildWireString("不应保存"))
	if !bytes.Equal(guildReply(t, peers[1], 154), []byte{0, 36}) {
		t.Fatal("revoked permission remained effective")
	}
	r, _ = dnfguild.Load(ctx, repos.Settings)
	if r.Guilds[1].Notice != "获授权公告" || r.Guilds[1].JoinMode != 0 || r.Guilds[1].MasterCharacterID != "1" {
		t.Fatal("permission edits lost committed settings")
	}
}

func TestGuildGradePermissionsRejectsInvalidOrUnauthorizedRequests(t *testing.T) {
	for _, mode := range []string{"short", "trailing", "grade_zero", "grade_master", "grade_six", "unknown_bits", "member", "deputy", "wrong_account", "stale_master", "save_failure", "class_zero"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos := guildTextFixture(t)
			ctx := context.Background()
			actor := peers[0]
			body := guildGradePermissionsBody(4, 16)
			want := byte(48)
			r, _ := dnfguild.Load(ctx, repos.Settings)
			switch mode {
			case "short":
				body = body[:4]
			case "trailing":
				body = append(body, 0)
			case "grade_zero":
				body[0] = 0
			case "grade_master":
				body[0] = 1
			case "grade_six":
				body[0] = 6
			case "unknown_bits":
				body = guildGradePermissionsBody(4, 1<<18)
			case "member", "deputy":
				actor = peers[1]
				want = 36
				m := r.Guilds[1].Members["2"]
				if mode == "deputy" {
					m.Grade = 2
				}
				r.Guilds[1].Members["2"] = m
				r.Guilds[1].GradePermissions = map[byte]uint32{m.Grade: dnfguild.GradePermissionMask}
			case "wrong_account":
				actor.accountID = "wrong"
				want = 34
			case "stale_master":
				m := r.Guilds[1].Members["1"]
				m.AccountID = "old"
				r.Guilds[1].Members["1"] = m
				want = 34
			case "save_failure":
				broken := repos
				broken.Guild = guildTextFailUoW{repos.Guild}
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return broken, true }
				want = 34
			case "class_zero":
				want = 0
			}
			if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if mode == "class_zero" {
				if _, err := currentGameplayModules.DispatchUpper(s, actor, 748, 0, body); err != nil {
					t.Fatal(err)
				}
			} else {
				guildDispatch(t, s, actor, 748, body)
				if got := guildReply(t, actor, 748); !bytes.Equal(got, []byte{0, want}) {
					t.Fatalf("ACK=%x want=%d", got, want)
				}
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected permission update changed state")
			}
			for _, peer := range peers {
				if peer != actor && peer.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("rejection refreshed peers")
				}
			}
		})
	}
}
