package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func guildGradeNameBody(grade byte, name string) []byte {
	return append([]byte{grade}, guildWireString(name)...)
}

func guildManagementGradeNames(t *testing.T, body []byte) []string {
	t.Helper()
	drop := func(n int) {
		t.Helper()
		if n < 0 || len(body) < n {
			t.Fatal("truncated management packet")
		}
		body = body[n:]
	}
	dstr := func() {
		t.Helper()
		if len(body) < 4 {
			t.Fatal("short management string")
		}
		n := binary.LittleEndian.Uint32(body)
		drop(4)
		drop(int(n))
	}
	dstr()
	drop(5)
	dstr()
	drop(8)
	dstr()
	dstr()
	drop(2)
	dstr()
	var names []string
	for grade := 0; grade < 6; grade++ {
		drop(4)
		if len(body) < 24 {
			t.Fatal("short grade record")
		}
		end := bytes.IndexByte(body[:24], 0)
		if end < 0 {
			t.Fatal("grade record lacks native NUL terminator")
		}
		name, err := simplifiedchinese.GB18030.NewDecoder().String(string(body[:end]))
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
		drop(24)
	}
	return names
}

func TestGuildGradeNamesNativeFlow(t *testing.T) {
	s, peers, repos := guildTextFixture(t)
	ctx := context.Background()
	before, _ := dnfguild.Load(ctx, repos.Settings)
	want := " " + strings.Repeat("中", 11) // exactly 23 GB18030 bytes, spacing retained
	guildDispatch(t, s, peers[0], 749, guildGradeNameBody(1, want))
	if got := guildReply(t, peers[0], 749); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("ACK=%x", got)
	}
	r, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil || r.Guilds[1].GradeNames[1] != want {
		t.Fatalf("name not persisted: %v", err)
	}
	for _, peer := range peers[:2] {
		names := guildManagementGradeNames(t, guildReply(t, peer, 70))
		if names[1] != want || names[2] != before.Guilds[1].GradeNames[2] || names[4] != before.Guilds[1].GradeNames[4] {
			t.Fatalf("native name truncated or adjacent role changed: %q", names)
		}
	}
	if peers[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("name broadcast crossed guilds")
	}
	if !reflect.DeepEqual(r.Guilds[1].Members, before.Guilds[1].Members) ||
		!reflect.DeepEqual(r.Guilds[1].GradePermissions, before.Guilds[1].GradePermissions) ||
		r.Guilds[1].MasterName != before.Guilds[1].MasterName {
		t.Fatal("role name edit changed member identity or permissions")
	}
	guildDispatch(t, s, peers[0], 749, guildGradeNameBody(1, want))
	if !bytes.Equal(guildReply(t, peers[0], 749), []byte{1}) {
		t.Fatal("same name replay failed")
	}
	after, _ := dnfguild.Load(ctx, repos.Settings)
	if !reflect.DeepEqual(r, after) {
		t.Fatal("same name replay changed persisted state")
	}
}

func TestGuildGradeNamesRejectsInvalidAndUnauthorized(t *testing.T) {
	for _, mode := range []string{"short", "trailing", "length", "bad_encoding", "nul", "newline", "blank", "utf16_limit", "wire_limit", "grade_zero", "grade_six", "member", "deputy", "wrong_account", "stale_master", "save_failure", "class_zero"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos := guildTextFixture(t)
			ctx := context.Background()
			actor, body, want := peers[0], guildGradeNameBody(4, "成员新称"), byte(159)
			r, _ := dnfguild.Load(ctx, repos.Settings)
			switch mode {
			case "short":
				body = body[:3]
			case "trailing":
				body = append(body, 0)
			case "length":
				body[1]++
			case "bad_encoding":
				body = []byte{4, 1, 0, 0, 0, 0x81}
			case "nul":
				body = guildGradeNameBody(4, "a\x00b")
			case "newline":
				body = guildGradeNameBody(4, "a\nb")
			case "blank":
				body = guildGradeNameBody(4, " \u3000 ")
			case "utf16_limit":
				body = guildGradeNameBody(4, strings.Repeat("a", 21))
			case "wire_limit":
				body = guildGradeNameBody(4, strings.Repeat("中", 12))
			case "grade_zero":
				body[0] = 0
			case "grade_six":
				body[0] = 6
			case "member", "deputy":
				actor, want = peers[1], 36
				m := r.Guilds[1].Members["2"]
				if mode == "deputy" {
					m.Grade = 2
				}
				r.Guilds[1].Members["2"] = m
				r.Guilds[1].GradePermissions = map[byte]uint32{m.Grade: dnfguild.GradePermissionMask}
			case "wrong_account":
				actor.accountID, want = "wrong", 34
			case "stale_master":
				m := r.Guilds[1].Members["1"]
				m.AccountID = "old"
				r.Guilds[1].Members["1"], want = m, 34
			case "save_failure":
				broken := repos
				broken.Guild = guildTextFailUoW{repos.Guild}
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return broken, true }
				want = 34
			}
			if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if mode == "class_zero" {
				if _, err := currentGameplayModules.DispatchUpper(s, actor, 749, 0, body); err != nil {
					t.Fatal(err)
				}
			} else {
				guildDispatch(t, s, actor, 749, body)
				if got := guildReply(t, actor, 749); !bytes.Equal(got, []byte{0, want}) {
					t.Fatalf("ACK=%x want=%d", got, want)
				}
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected name changed registry")
			}
			for _, peer := range peers {
				if peer != actor && peer.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("rejected name refreshed peers")
				}
			}
		})
	}
}
