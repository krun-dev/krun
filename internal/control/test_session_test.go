package control

import (
	"context"
	"testing"
	"time"
)

// insertTestSession fills the common fixture defaults and persists a session.
func insertTestSession(t *testing.T, store *Store, token string, session Session) {
	t.Helper()
	if session.AuthVersion == 0 {
		session.AuthVersion = 1
	}
	if session.Created.IsZero() {
		session.Created = now()
	}
	if session.Seen.IsZero() {
		session.Seen = session.Created
	}
	if session.Absolute.IsZero() {
		session.Absolute = session.Created.Add(time.Hour)
	}
	if err := store.InsertSession(context.Background(), token, session); err != nil {
		t.Fatal(err)
	}
}
