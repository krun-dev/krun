package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStateJSONExcludesPendingCommandsAndAuthorization(t *testing.T) {
	state := NewState()
	state.Authorization = NewAuthorizationCache()
	state.PendingLogin = &LoginCommit{Session: Session{CSRF: "private-csrf"}}
	state.PendingAudits = []Audit{{Actor: "private-pending-actor"}}
	state.PendingWorkerSecrets = map[string]EncryptedWorkerSecret{
		"secret": {Ciphertext: []byte("private-ciphertext"), Nonce: []byte("private-nonce")},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"Authorization", "PendingLogin", "PendingAudits", "PendingWorkerSecrets", "private-"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("request-local data leaked into JSON: %s", field)
		}
	}
	clone, err := CloneState(state)
	if err != nil {
		t.Fatal(err)
	}
	if clone.Authorization != nil || clone.PendingLogin != nil || len(clone.PendingAudits) != 0 || len(clone.PendingWorkerSecrets) != 0 {
		t.Fatal("snapshot clone retained request-local commands")
	}
}
