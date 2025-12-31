package records

import (
	"context"
	"errors"
	"net/url"
	"os"

	"github.com/fxamacker/cbor/v2"
	"github.com/juicebox-systems/juicebox-software-realm/otel"
	"github.com/juicebox-systems/juicebox-software-realm/types"
	"github.com/juicebox-systems/juicebox-software-realm/volcenginekms"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.opentelemetry.io/otel/trace"
)

type MongoRecordStore struct {
	client       *mongo.Client
	databaseName string
	kmsClient    *volcenginekms.Client
}

const userRecordsCollection string = "userRecords"
const serializedUserRecordKey string = "serializedUserRecord"
const versionKey string = "version"
const encryptedDataKeyKey string = "encryptedDataKey"
const encryptionNonceKey string = "encryptionNonce"
const encryptionVersionKey string = "encryptionVersion"

func NewMongoRecordStore(ctx context.Context, realmID types.RealmID) (*MongoRecordStore, error) {
	ctx, span := otel.StartSpan(
		ctx,
		"NewMongoRecordStore",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(semconv.DBSystemMongoDB),
	)
	defer span.End()

	urlString := os.Getenv("MONGO_URL")
	if urlString == "" {
		err := errors.New("unexpectedly missing MONGO_URL")
		return nil, otel.RecordOutcome(err, span)
	}

	url, err := url.Parse(urlString)
	if err != nil {
		return nil, otel.RecordOutcome(err, span)
	}

	databaseName := types.JuiceboxRealmDatabasePrefix + realmID.String()

	// mongodb urls traditionally end in "/database", so we extract any
	// provided database name here (stripping the leading "/").
	if len(url.Path) > 1 {
		databaseName = url.Path[1:]
	}

	clientOptions := options.Client().ApplyURI(urlString)
	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		return nil, otel.RecordOutcome(err, span)
	}

	err = client.Database(databaseName).CreateCollection(ctx, userRecordsCollection)
	if err != nil {
		// ignore the "NamespaceExists" error code
		if mErr, ok := err.(mongo.CommandError); !ok || !mErr.HasErrorCode(48) {
			return nil, otel.RecordOutcome(err, span)
		}
	}

	kmsClient, err := volcenginekms.NewClient(ctx, realmID)
	if err != nil {
		return nil, otel.RecordOutcome(err, span)
	}

	return &MongoRecordStore{
		client:       client,
		databaseName: databaseName,
		kmsClient:    kmsClient,
	}, nil
}

func (m MongoRecordStore) GetRecord(ctx context.Context, recordID UserRecordID) (UserRecord, interface{}, error) {
	ctx, span := otel.StartSpan(
		ctx,
		"GetRecord",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(semconv.DBSystemMongoDB),
	)
	defer span.End()

	userRecord := DefaultUserRecord()

	database := m.client.Database(m.databaseName)
	collection := database.Collection(userRecordsCollection)

	var result bson.M
	err := collection.FindOne(
		ctx,
		bson.M{"_id": recordID},
	).Decode(&result)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			// no stored record yet
			return userRecord, nil, nil
		}
		return userRecord, nil, otel.RecordOutcome(err, span)
	}

	record, ok := result[serializedUserRecordKey]
	if !ok {
		err := errors.New("result unexpectedly missing 'serializedUserRecord' key")
		return userRecord, result, otel.RecordOutcome(err, span)
	}

	primitiveBinaryRecord, ok := record.(primitive.Binary)
	if !ok {
		err := errors.New("user record was of wrong type")
		return userRecord, result, otel.RecordOutcome(err, span)
	}

	serializedUserRecord := primitiveBinaryRecord.Data
	envelope, err := readEnvelope(result, serializedUserRecord)
	if err != nil {
		return userRecord, result, otel.RecordOutcome(err, span)
	}

	if envelope != nil {
		serializedUserRecord, err = m.kmsClient.Decrypt(ctx, envelope)
		if err != nil {
			return userRecord, result, otel.RecordOutcome(err, span)
		}
	}

	err = cbor.Unmarshal(serializedUserRecord, &userRecord)
	if err != nil {
		return userRecord, result, otel.RecordOutcome(err, span)
	}

	return userRecord, result, nil
}

func (m MongoRecordStore) WriteRecord(ctx context.Context, recordID UserRecordID, record UserRecord, readRecord interface{}) error {
	ctx, span := otel.StartSpan(
		ctx,
		"WriteRecord",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(semconv.DBSystemMongoDB),
	)
	defer span.End()

	database := m.client.Database(m.databaseName)
	collection := database.Collection(userRecordsCollection)

	serializedUserRecord, err := cbor.Marshal(record)
	if err != nil {
		return otel.RecordOutcome(err, span)
	}

	envelope, err := m.kmsClient.Encrypt(ctx, serializedUserRecord)
	if err != nil {
		return otel.RecordOutcome(err, span)
	}

	// mongo doesn't support unsigned integers, but it supports 64-bit signed
	// integers. since this version can safely overflow we don't care.
	var newVersion int64
	var previousVersion *int64

	// If we read an existing record from the db, try and identify a version for it.
	// We'll use this version to ensure no-one has mutated this row since we read it.
	if readRecord != nil {
		readRecord, ok := readRecord.(primitive.M)
		if !ok {
			err := errors.New("unexepected type for read record")
			return otel.RecordOutcome(err, span)
		}

		record, ok := readRecord[versionKey]
		if !ok {
			err := errors.New("read record unexpectedly missing version attribute")
			return otel.RecordOutcome(err, span)
		}

		v, ok := record.(int64)
		if !ok {
			err := errors.New("read record version key was of wrong type")
			return otel.RecordOutcome(err, span)
		}

		newVersion = v + 1
		previousVersion = &v
	}

	_, err = collection.UpdateOne(
		ctx,
		// lookup a record based on the recordID and previousVersion (or nil version)
		bson.M{
			"_id":      recordID,
			versionKey: previousVersion,
		},
		// and set these keys on that record if we find it
		bson.M{
			"$set": bson.M{
				"_id":                   recordID,
				serializedUserRecordKey: envelope.Ciphertext,
				encryptedDataKeyKey:     envelope.EncryptedDataKey,
				encryptionNonceKey:      envelope.Nonce,
				encryptionVersionKey:    envelope.Version,
				versionKey:              newVersion,
			},
		},
		// if we find no record set the keys on a new record if previousVersion was nil
		options.Update().SetUpsert(previousVersion == nil),
	)

	if err != nil {
		return otel.RecordOutcome(err, span)
	}
	return nil
}

func readEnvelope(result bson.M, ciphertext []byte) (*volcenginekms.Envelope, error) {
	encryptedKeyValue, hasEncryptedKey := result[encryptedDataKeyKey]
	nonceValue, hasNonce := result[encryptionNonceKey]
	versionValue, hasVersion := result[encryptionVersionKey]
	if !hasEncryptedKey && !hasNonce && !hasVersion {
		return nil, nil
	}

	encryptedKey, ok := encryptedKeyValue.(string)
	if !ok || encryptedKey == "" {
		return nil, errors.New("encrypted data key was missing or invalid")
	}

	nonceBinary, ok := nonceValue.(primitive.Binary)
	if !ok || len(nonceBinary.Data) == 0 {
		return nil, errors.New("encryption nonce was missing or invalid")
	}

	version, err := parseEncryptionVersion(versionValue)
	if err != nil {
		return nil, err
	}

	return &volcenginekms.Envelope{
		Ciphertext:       ciphertext,
		EncryptedDataKey: encryptedKey,
		Nonce:            nonceBinary.Data,
		Version:          version,
	}, nil
}

func parseEncryptionVersion(versionValue interface{}) (int, error) {
	switch v := versionValue.(type) {
	case int32:
		return int(v), nil
	case int64:
		return int(v), nil
	case int:
		return v, nil
	case nil:
		return 0, errors.New("encryption version missing")
	default:
		return 0, errors.New("encryption version was of wrong type")
	}
}
