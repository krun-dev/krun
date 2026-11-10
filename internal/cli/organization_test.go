package cli

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestOrganizationCommandRejectsUnsafeInputs(t *testing.T) {
	client := Client{Profile: Profile{Organization: "cli-team"}, Out: io.Discard, Err: io.Discard}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "implicit unrestricted invitation",
			args: []string{"invitations", "create", "--target-user", " "},
			want: "--target-user must not be empty",
		},
		{
			name: "both invitation recipient selectors",
			args: []string{"invitations", "create", "--target-user", "user", "--target-email", "user@example.com"},
			want: "choose either --target-user or --target-email",
		},
		{
			name: "invalid invitation role",
			args: []string{"invitations", "create", "--role", "org_admin"},
			want: "--role must be member or viewer",
		},
		{
			name: "zero invitation quota",
			args: []string{"invitations", "create", "--max-uses", "0"},
			want: "--max-uses must be between 1 and 100",
		},
		{
			name: "empty allowed source",
			args: []string{"update", "--allowed-source", " "},
			want: "source ID is required",
		},
		{
			name: "conflicting source options",
			args: []string{"update", "--allowed-source", "local", "--clear-allowed-sources"},
			want: "choose either --allowed-source or --clear-allowed-sources",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := client.organization(context.Background(), test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v, want %q", err, test.want)
			}
		})
	}
}
