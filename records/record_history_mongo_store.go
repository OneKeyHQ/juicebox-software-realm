package records

import (
	"context"
	"errors"
	"log"
	"net/url"
	"os"
	"time"

	"github.com/juicebox-systems/juicebox-software-realm/otel"
	"github.com/juicebox-systems/juicebox-software-realm/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.opentelemetry.io/otel/trace"
)

const userRecordHistoryCollection string = "userRecordHistory"

// Kept small and separate from the record store's pool so that backups can
// never take connections away from serving requests. The minimum is reset too,
// as one inherited from MONGO_URL could exceed the maximum and fail validation.
const recordHistoryMinPoolSize uint64 = 0
const recordHistoryMaxPoolSize uint64 = 4
const recordHistorySetupTimeout = 10 * time.Second

type MongoRecordHistoryStore struct {
	client       *mongo.Client
	databaseName string
}

func NewMongoRecordHistoryStore(ctx context.Context, realmID types.RealmID) (*MongoRecordHistoryStore, error) {
	ctx, span := otel.StartSpan(
		ctx,
		"NewMongoRecordHistoryStore",
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

	client, err := mongo.Connect(ctx, recordHistoryClientOptions(urlString))
	if err != nil {
		return nil, otel.RecordOutcome(err, span)
	}

	// The collection and index are a convenience: inserts create the
	// collection on demand, so failing here must not stop the realm.
	setupCtx, cancel := context.WithTimeout(ctx, recordHistorySetupTimeout)
	defer cancel()

	database := client.Database(databaseName)
	err = database.CreateCollection(setupCtx, userRecordHistoryCollection)
	if err != nil {
		// ignore the "NamespaceExists" error code
		if mErr, ok := err.(mongo.CommandError); !ok || !mErr.HasErrorCode(48) {
			log.Printf("record history: failed to create collection: %v", err)
		}
	}

	_, err = database.Collection(userRecordHistoryCollection).Indexes().CreateOne(setupCtx, mongo.IndexModel{
		Keys: bson.D{{Key: "userRecordId", Value: 1}, {Key: "archivedAt", Value: -1}},
	})
	if err != nil {
		log.Printf("record history: failed to create index: %v", err)
	}

	return &MongoRecordHistoryStore{
		client:       client,
		databaseName: databaseName,
	}, nil
}

func recordHistoryClientOptions(urlString string) *options.ClientOptions {
	return options.Client().
		ApplyURI(urlString).
		SetMinPoolSize(recordHistoryMinPoolSize).
		SetMaxPoolSize(recordHistoryMaxPoolSize)
}

func (m MongoRecordHistoryStore) Insert(ctx context.Context, entry RecordHistoryEntry) error {
	ctx, span := otel.StartSpan(
		ctx,
		"RecordHistory.Insert",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(semconv.DBSystemMongoDB),
	)
	defer span.End()

	database := m.client.Database(m.databaseName)
	collection := database.Collection(userRecordHistoryCollection)

	_, err := collection.InsertOne(ctx, entry)
	if err != nil {
		return otel.RecordOutcome(err, span)
	}
	return nil
}
