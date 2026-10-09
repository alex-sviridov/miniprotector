package clientmanager

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateRole_AcceptsEachValidToken(t *testing.T) {
	for _, role := range ValidRoles {
		assert.NoError(t, ValidateRole(role))
	}
}

func TestValidateRole_AcceptsMultiRoleCommaList(t *testing.T) {
	assert.NoError(t, ValidateRole("store,client"))
}

func TestValidateRole_RejectsUnknownToken(t *testing.T) {
	assert.Error(t, ValidateRole("web"))
}

func TestValidateRole_RejectsEmptyValue(t *testing.T) {
	assert.Error(t, ValidateRole(""))
}

func TestValidateRole_RejectsOneBadTokenInCommaList(t *testing.T) {
	assert.Error(t, ValidateRole("client,bogus"))
}
