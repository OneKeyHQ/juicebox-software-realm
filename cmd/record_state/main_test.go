package main

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRedactCredentials(t *testing.T) {
	for _, mongoURL := range []string{
		"mongodb://admin:s3cret@db.internal:27017/jb%ZZ",
		"mongodb://admin:sec/ret@db.internal:27017/jb",
		"mongodb://admin:p@ss@db.internal:27017/jb%ZZ",
		"mongodb://admin:qu\"ote\\d@db.internal:27017/jb%ZZ",
		"mongodb+srv://admin:s3cret@db.internal/jb",
	} {
		_, parseErr := url.Parse(mongoURL)
		messages := []string{"connect " + mongoURL + ": failed"}
		if parseErr != nil {
			// url.Error quotes the URL, escaping some characters.
			messages = append(messages, parseErr.Error())
		}
		for _, message := range messages {
			redacted := redactCredentials(message, mongoURL)
			assert.NotContains(t, redacted, "admin", mongoURL)
			for _, password := range []string{"s3cret", "sec/ret", "p@ss", "qu\"ote\\d", `qu\"ote\\d`} {
				assert.NotContains(t, redacted, password, mongoURL)
			}
			assert.Contains(t, redacted, "db.internal", mongoURL)
		}
	}

	assert.Equal(t, "no credentials", redactCredentials("no credentials", "mongodb://host/db"))
	assert.Equal(t, "no url", redactCredentials("no url", ""))
}

func TestLoadEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".juicebox-realm.env")
	contents := `# a comment
; another comment

REALM_ID=0102030405060708090a0b0c0d0e0f10
MONGO_URL=mongodb://user:p@ss@host:27017/juicebox?authSource=admin&replicaSet=rs0
 VOLCENGINE_REGION = cn-beijing
VOLCENGINE_KMS_KEY_ID="quoted key"
VOLCSTACK_ACCESS_KEY_ID='single quoted'
EMPTY=
`
	assert.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	for _, key := range []string{"REALM_ID", "MONGO_URL", "VOLCENGINE_REGION", "VOLCENGINE_KMS_KEY_ID", "VOLCSTACK_ACCESS_KEY_ID", "EMPTY"} {
		t.Setenv(key, "unset")
	}

	assert.NoError(t, loadEnvFile(path))
	assert.Equal(t, "0102030405060708090a0b0c0d0e0f10", os.Getenv("REALM_ID"))
	assert.Equal(t, "mongodb://user:p@ss@host:27017/juicebox?authSource=admin&replicaSet=rs0", os.Getenv("MONGO_URL"))
	assert.Equal(t, "cn-beijing", os.Getenv("VOLCENGINE_REGION"))
	assert.Equal(t, "quoted key", os.Getenv("VOLCENGINE_KMS_KEY_ID"))
	assert.Equal(t, "single quoted", os.Getenv("VOLCSTACK_ACCESS_KEY_ID"))
	assert.Equal(t, "", os.Getenv("EMPTY"))
}

func TestLoadEnvFileErrorsDontQuoteContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.env")
	assert.NoError(t, os.WriteFile(path, []byte("OK=1\nmongodb://user:s3cret@host\n"), 0o600))
	err := loadEnvFile(path)
	assert.EqualError(t, err, "line 2 isn't KEY=VALUE")

	assert.Error(t, loadEnvFile(filepath.Join(t.TempDir(), "missing.env")))
}
