package records

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"

	"github.com/juicebox-systems/juicebox-software-realm/types"
	"github.com/juicebox-systems/juicebox-software-realm/volcenginekms"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

// MongoRecordInspector reads user records from a realm's Mongo database the
// same way MongoRecordStore does, for operational tooling. Unlike
// NewMongoRecordStore it never creates anything, and it always reads from
// the primary so it sees the latest writes.
type MongoRecordInspector struct {
	store        MongoRecordStore
	DatabaseName string
}

func NewMongoRecordInspector(ctx context.Context, realmID types.RealmID) (*MongoRecordInspector, error) {
	urlString := os.Getenv("MONGO_URL")
	if urlString == "" {
		return nil, errors.New("unexpectedly missing MONGO_URL")
	}

	// Errors from parsing MONGO_URL or applying it can quote parts of it,
	// credentials included, so they aren't passed on.
	url, err := url.Parse(urlString)
	if err != nil {
		return nil, errors.New("MONGO_URL is not a valid URL")
	}

	databaseName := types.JuiceboxRealmDatabasePrefix + realmID.String()

	// mongodb urls traditionally end in "/database", so we extract any
	// provided database name here (stripping the leading "/").
	if len(url.Path) > 1 {
		databaseName = url.Path[1:]
	}

	client, err := mongo.Connect(ctx, inspectorClientOptions(urlString))
	if err != nil {
		return nil, errors.New("MONGO_URL was rejected by the mongo driver, check its options")
	}

	if _, err := primaryElection(ctx, client); err != nil {
		return nil, err
	}

	// Catch a wrong database up front rather than reporting a missing record.
	names, err := client.Database(databaseName).ListCollectionNames(ctx, bson.M{"name": userRecordsCollection})
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("database %q has no %s collection", databaseName, userRecordsCollection)
	}

	kmsClient, err := volcenginekms.NewClient(ctx, realmID)
	if err != nil {
		return nil, err
	}

	return &MongoRecordInspector{
		store: MongoRecordStore{
			client:       client,
			databaseName: databaseName,
			kmsClient:    kmsClient,
		},
		DatabaseName: databaseName,
	}, nil
}

func inspectorClientOptions(urlString string) *options.ClientOptions {
	return options.Client().ApplyURI(urlString).SetReadPreference(readpref.Primary())
}

// GetRecord behaves like MongoRecordStore.GetRecord, but fails unless the
// node it read from was the primary throughout the read, in a single term.
func (i *MongoRecordInspector) GetRecord(ctx context.Context, recordID UserRecordID) (UserRecord, interface{}, error) {
	before, err := primaryElection(ctx, i.store.client)
	if err != nil {
		return DefaultUserRecord(), nil, err
	}
	record, readRecord, err := i.store.GetRecord(ctx, recordID)
	if err != nil {
		return DefaultUserRecord(), nil, err
	}
	after, err := primaryElection(ctx, i.store.client)
	if err != nil {
		return DefaultUserRecord(), nil, err
	}
	if before != after {
		return DefaultUserRecord(), nil, errors.New("a mongo primary election happened during the read, try again")
	}
	return record, readRecord, nil
}

// The primary read preference doesn't apply to a direct connection, which
// always talks to the one node, so check that node is the primary. Returns the
// id of the election that made it primary, which changes with every election;
// it's zero for a standalone server or mongos, which have no elections.
func primaryElection(ctx context.Context, client *mongo.Client) (primitive.ObjectID, error) {
	var isMaster struct {
		IsMaster   bool               `bson:"ismaster"`
		ElectionID primitive.ObjectID `bson:"electionId"`
	}
	err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "isMaster", Value: 1}}).Decode(&isMaster)
	if err != nil {
		return primitive.ObjectID{}, err
	}
	if !isMaster.IsMaster {
		return primitive.ObjectID{}, errors.New("the mongo node isn't the primary, so its data may be stale; don't use directConnection to a secondary")
	}
	return isMaster.ElectionID, nil
}

// IsEncryptedRecord reports whether a raw record read from Mongo is stored
// encrypted, as opposed to a plaintext record from before KMS encryption.
// Only encrypted records are bound to their realm by the KMS context.
func IsEncryptedRecord(readRecord interface{}) bool {
	raw, ok := readRecord.(primitive.M)
	if !ok {
		return false
	}
	_, ok = raw[encryptedDataKeyKey]
	return ok
}
