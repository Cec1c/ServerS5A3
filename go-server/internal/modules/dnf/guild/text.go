package guild

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The matched client reads these DSTRs into 256- and 21-byte buffers.
const NoticeMaxBytes = 255
const MemberMemoMaxBytes = 20
const NoticePermission = uint32(1 << 4)

var ErrInvalidText = errors.New("guild text is invalid")

func validateText(text string, limit int, notice bool) error {
	if !utf8.ValidString(text) || (!notice && strings.TrimSpace(text) == "") {
		return ErrInvalidText
	}
	for _, r := range text {
		if unicode.IsControl(r) && !(notice && (r == '\r' || r == '\n')) {
			return ErrInvalidText
		}
	}
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
	if err != nil || len(encoded) > limit {
		return ErrInvalidText
	}
	return nil
}

// SetNotice must run in Update's Guild UoW. Permissions come from the persisted
// grade mask; a grade number alone does not authorize editing the notice.
func (r *Registry) SetNotice(owner dnfrepo.GuildOwner, text string, now time.Time) (*Guild, error) {
	g, member, found := r.GuildForOwner(owner)
	if !found {
		return nil, ErrNotMember
	}
	if GradePermissionsFor(g, member.Grade)&NoticePermission == 0 {
		return nil, ErrPermissionDenied
	}
	if err := validateText(text, NoticeMaxBytes, true); err != nil {
		return nil, err
	}
	if g.Notice != text {
		g.Notice = text
		g.UpdatedAt = now
	}
	return g, nil
}

// SetMemberMemo edits only the authenticated member. The native request carries
// one DSTR and has no target member field.
func (r *Registry) SetMemberMemo(owner dnfrepo.GuildOwner, text string, now time.Time) (*Guild, error) {
	g, member, found := r.GuildForOwner(owner)
	if !found {
		return nil, ErrNotMember
	}
	if err := validateText(text, MemberMemoMaxBytes, false); err != nil {
		return nil, err
	}
	if member.Memo != text {
		member.Memo = text
		g.Members[owner.CharacterID] = member
		g.UpdatedAt = now
	}
	return g, nil
}
