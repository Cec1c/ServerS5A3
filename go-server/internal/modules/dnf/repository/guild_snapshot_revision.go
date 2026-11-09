package repository

import "context"

// A namespace belongs to one repository router, including its snapshot views.
// It contains no credentials and cannot be reused by an unrelated database.
type GuildSnapshotNamespace struct{ Reserved byte }

// GuildSnapshotRevisionReader returns the content fingerprint in the caller's
// current read view. A nil namespace requests the ordinary uncached path.
type GuildSnapshotRevisionReader interface {
	GuildSnapshotNamespace() *GuildSnapshotNamespace
	LoadGuildSnapshotRevision(context.Context) (string, bool, error)
}
