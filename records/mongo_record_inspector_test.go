package records

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

func TestInspectorClientOptionsReadFromPrimary(t *testing.T) {
	opts := inspectorClientOptions("mongodb://localhost:27017/db?readPreference=secondary")
	assert.NoError(t, opts.Validate())
	assert.Equal(t, readpref.PrimaryMode, opts.ReadPreference.Mode())
}

func TestIsEncryptedRecord(t *testing.T) {
	assert.True(t, IsEncryptedRecord(primitive.M{"serializedUserRecord": primitive.Binary{}, "encryptedDataKey": "key"}))
	assert.False(t, IsEncryptedRecord(primitive.M{"serializedUserRecord": primitive.Binary{}}))
	assert.False(t, IsEncryptedRecord(nil))
	assert.False(t, IsEncryptedRecord(DefaultUserRecord()))
}

func TestNewMongoRecordInspectorHidesMongoURL(t *testing.T) {
	for _, mongoURL := range []string{
		// url.Parse quotes a fragment of the password: invalid port ":sec".
		"mongodb://admin:sec/ret@db.internal:27017/jb",
		"mongodb:/admin:s3cret@db.internal:27017/jb%ZZ",
		// The driver rejects the option.
		"mongodb://admin:s3cret@db.internal:27017/jb?minPoolSize=nope",
	} {
		t.Setenv("MONGO_URL", mongoURL)
		_, err := NewMongoRecordInspector(context.Background(), testRealmID)
		assert.Error(t, err, mongoURL)
		for _, secret := range []string{"admin", "sec", "s3cret", "ret", "db.internal"} {
			assert.NotContains(t, err.Error(), secret, mongoURL)
		}
	}
}
