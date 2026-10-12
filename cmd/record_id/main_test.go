package main

import (
	"testing"

	"github.com/juicebox-systems/juicebox-software-realm/records"
	"github.com/stretchr/testify/assert"
)

// The expected ids were computed independently as
// hex(blake2s256(cbor({"tenant_name": tenant, "user_id": sub}))).
func TestRecordID(t *testing.T) {
	recordID, err := records.CreateUserRecordID("test", "121314")
	assert.NoError(t, err)
	assert.Equal(t, records.UserRecordID("44f39ff54b91c0cb6cbd8ae3747997cce395c6c0cddff2dfe9eb78b6f845192d"), recordID)

	recordID, err = records.CreateUserRecordID("onekey", "a-user-id")
	assert.NoError(t, err)
	assert.Equal(t, records.UserRecordID("f8c5efa3c13c7cd149614d4808c351a2ce79205c9e6635fc79cd4d041616243b"), recordID)
}

// The expected subs were computed independently with node, using the same
// template literal as service-juicebox's getIdFromMetadata.
func TestUserSub(t *testing.T) {
	assert.Equal(t, "443a3ea20747036ca6a0f1fa65ac0153a585ba15525d3c28d404cf7a84f65f38",
		userSub("test-salt", "google", "109876543210987654321", false))
	assert.Equal(t, "995e2bb3e10f2fd641977f357f47e63346195cc8424d45504138c01abd389cd0",
		userSub("test-salt", "apple", "001234.abcdef0123456789.0123", true))
}
