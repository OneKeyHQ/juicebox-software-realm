package volcenginekms

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"

	"github.com/juicebox-systems/juicebox-software-realm/types"
	volckms "github.com/volcengine/volcengine-go-sdk/service/kms"
	"github.com/volcengine/volcengine-go-sdk/volcengine"
	"github.com/volcengine/volcengine-go-sdk/volcengine/credentials"
	"github.com/volcengine/volcengine-go-sdk/volcengine/session"
)

const (
	envVolcengineRegion  = "VOLCENGINE_REGION"
	envVolcengineKmsKey  = "VOLCENGINE_KMS_KEY_ID"
	envelopeKeyBytes     = 32
	envelopeVersion      = 1
	envelopeContextRealm = "realm_id"
)

type Envelope struct {
	Ciphertext       []byte
	EncryptedDataKey string
	Nonce            []byte
	Version          int
}

type Client struct {
	api               volckms.KMSAPI
	keyID             string
	encryptionContext map[string]*string
}

func NewClient(ctx context.Context, realmID types.RealmID) (*Client, error) {
	region := os.Getenv(envVolcengineRegion)
	if region == "" {
		return nil, errors.New("unexpectedly missing VOLCENGINE_REGION")
	}

	keyID := os.Getenv(envVolcengineKmsKey)
	if keyID == "" {
		return nil, errors.New("unexpectedly missing VOLCENGINE_KMS_KEY_ID")
	}

	config := volcengine.NewConfig().
		WithRegion(region).
		WithCredentials(credentials.NewEnvCredentials())

	sess, err := session.NewSession(config)
	if err != nil {
		return nil, err
	}

	realmIDString := realmID.String()
	encryptionContext := map[string]*string{
		envelopeContextRealm: &realmIDString,
	}

	return &Client{
		api:               volckms.New(sess),
		keyID:             keyID,
		encryptionContext: encryptionContext,
	}, nil
}

func (c *Client) Encrypt(ctx context.Context, plaintext []byte) (*Envelope, error) {
	numBytes := int32(envelopeKeyBytes)
	output, err := c.api.GenerateDataKeyWithContext(ctx, &volckms.GenerateDataKeyInput{
		KeyID:             &c.keyID,
		NumberOfBytes:     &numBytes,
		EncryptionContext: c.encryptionContext,
	})
	if err != nil {
		return nil, err
	}
	if output == nil || output.Plaintext == nil || output.CiphertextBlob == nil {
		return nil, errors.New("kms data key response missing plaintext or ciphertext")
	}

	dataKey, err := decodeMaybeBase64(*output.Plaintext)
	if err != nil {
		return nil, err
	}
	if !isValidAESKey(dataKey) {
		return nil, errors.New("kms data key was invalid size")
	}

	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	return &Envelope{
		Ciphertext:       ciphertext,
		EncryptedDataKey: *output.CiphertextBlob,
		Nonce:            nonce,
		Version:          envelopeVersion,
	}, nil
}

func (c *Client) Decrypt(ctx context.Context, envelope *Envelope) ([]byte, error) {
	if envelope == nil {
		return nil, errors.New("missing envelope")
	}
	if envelope.Version != envelopeVersion {
		return nil, errors.New("unsupported envelope version")
	}
	if envelope.EncryptedDataKey == "" {
		return nil, errors.New("missing encrypted data key")
	}
	if len(envelope.Nonce) == 0 {
		return nil, errors.New("missing nonce")
	}

	output, err := c.api.DecryptWithContext(ctx, &volckms.DecryptInput{
		CiphertextBlob:    &envelope.EncryptedDataKey,
		EncryptionContext: c.encryptionContext,
	})
	if err != nil {
		return nil, err
	}
	if output == nil || output.Plaintext == nil {
		return nil, errors.New("kms decrypt response missing plaintext")
	}

	dataKey, err := decodeMaybeBase64(*output.Plaintext)
	if err != nil {
		return nil, err
	}
	if !isValidAESKey(dataKey) {
		return nil, errors.New("kms data key was invalid size")
	}

	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return gcm.Open(nil, envelope.Nonce, envelope.Ciphertext, nil)
}

func decodeMaybeBase64(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("empty value")
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return []byte(value), nil
}

func isValidAESKey(key []byte) bool {
	switch len(key) {
	case 16, 24, 32:
		return true
	default:
		return false
	}
}
