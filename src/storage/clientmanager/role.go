// role.go defines the closed authorization-role enum a node is assigned
// at enrollment and carries in its operating certificate's attribute
// extension under the "authz-role" key. See
// docs/superpowers/specs/2026-08-22-role-based-grpc-authz-design.md.
package clientmanager

import (
	"fmt"
	"strings"
)

// RoleAttributeKey is the reserved ClientKVRecord (KindAttribute) key
// carrying a node's authorization role(s). Deliberately not the bare
// "role" -- that key is already used as a free-form policy-targeting
// label (see demo/policy-server/policies/backup/webserver-backup.json).
const RoleAttributeKey = "authz-role"

// DefaultRole is assigned when client-manager add / AddClient is not
// given an explicit role -- the common case, since most enrolled nodes
// are ordinary backup-agent hosts.
const DefaultRole = "client"

// ValidRoles is the closed set of roles a node may be assigned.
var ValidRoles = []string{"control-plane", "store", "client"}

// ValidateRole checks that value is a non-empty comma-separated list of
// ValidRoles tokens. Called wherever a RoleAttributeKey value is
// written: Store.SetKV, and the CLI/clientmanager-admin-api call sites
// that set it alongside AddClient/ReEnrollClient.
func ValidateRole(value string) error {
	if value == "" {
		return fmt.Errorf("role must not be empty")
	}
	for _, token := range strings.Split(value, ",") {
		if !isValidRoleToken(token) {
			return fmt.Errorf("invalid role %q: must be one of %v", token, ValidRoles)
		}
	}
	return nil
}

func isValidRoleToken(token string) bool {
	for _, r := range ValidRoles {
		if token == r {
			return true
		}
	}
	return false
}
