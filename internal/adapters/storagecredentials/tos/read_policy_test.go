package tos

import (
	"encoding/json"
	"testing"

	"github.com/krun-dev/krun/internal/domain"
)

func TestImmutableReadPolicyHasNoWriteListOrAdjacentPrefix(t *testing.T) {
	read := scope()
	read.Access = domain.StorageRead
	read.Prefix = "installation/pub/org/worker/digest/"
	raw, err := sessionPolicy(read)
	if err != nil {
		t.Fatal(err)
	}
	var policy struct{ Statement []statement }
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		t.Fatal(err)
	}
	if len(policy.Statement) != 1 || len(policy.Statement[0].Action) != 1 ||
		policy.Statement[0].Action[0] != "tos:GetObject" ||
		len(policy.Statement[0].Resource) != 1 ||
		policy.Statement[0].Resource[0] != "trn:tos:::example-bucket/installation/pub/org/worker/digest/*" {
		t.Fatalf("incorrect immutable read policy: %s", raw)
	}
	for _, prefix := range []string{"installation/pub/org/worker/digest", "installation/pub/*/worker/", "installation/../v1/"} {
		read.Prefix = prefix
		if _, err := sessionPolicy(read); err == nil {
			t.Fatal("unsafe read prefix accepted")
		}
	}
}
