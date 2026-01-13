package records

import (
	"context"
	"errors"
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

const pinAttemptsCollection string = "pinAttempts"

type MongoPinAttemptStore struct {
	client       *mongo.Client
	databaseName string
}

type mongoPinAttempt struct {
	ID       string    `bson:"_id"`
	TryCount int       `bson:"try_count"`
	RetryAt  time.Time `bson:"retry_at"`
}

func NewMongoPinAttemptStore(ctx context.Context, realmID types.RealmID) (*MongoPinAttemptStore, error) {
	ctx, span := otel.StartSpan(
		ctx,
		"NewMongoPinAttemptStore",
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

	err = client.Database(databaseName).CreateCollection(ctx, pinAttemptsCollection)
	if err != nil {
		// ignore the "NamespaceExists" error code
		if mErr, ok := err.(mongo.CommandError); !ok || !mErr.HasErrorCode(48) {
			return nil, otel.RecordOutcome(err, span)
		}
	}

	return &MongoPinAttemptStore{
		client:       client,
		databaseName: databaseName,
	}, nil
}

func (m MongoPinAttemptStore) Get(ctx context.Context, userID string) (PinAttempt, error) {
	ctx, span := otel.StartSpan(
		ctx,
		"PinAttempt.Get",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(semconv.DBSystemMongoDB),
	)
	defer span.End()

	database := m.client.Database(m.databaseName)
	collection := database.Collection(pinAttemptsCollection)

	var result mongoPinAttempt
	err := collection.FindOne(
		ctx,
		bson.M{"_id": userID},
	).Decode(&result)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return PinAttempt{}, nil
		}
		return PinAttempt{}, otel.RecordOutcome(err, span)
	}

	return PinAttempt{
		TryCount: result.TryCount,
		RetryAt:  result.RetryAt,
	}, nil
}

func (m MongoPinAttemptStore) Upsert(ctx context.Context, userID string, attempt PinAttempt) error {
	ctx, span := otel.StartSpan(
		ctx,
		"PinAttempt.Upsert",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(semconv.DBSystemMongoDB),
	)
	defer span.End()

	database := m.client.Database(m.databaseName)
	collection := database.Collection(pinAttemptsCollection)

	_, err := collection.UpdateOne(
		ctx,
		bson.M{"_id": userID},
		bson.M{
			"$set": bson.M{
				"_id":       userID,
				"try_count": attempt.TryCount,
				"retry_at":  attempt.RetryAt,
			},
		},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return otel.RecordOutcome(err, span)
	}

	return nil
}

func (m MongoPinAttemptStore) Delete(ctx context.Context, userID string) error {
	ctx, span := otel.StartSpan(
		ctx,
		"PinAttempt.Delete",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(semconv.DBSystemMongoDB),
	)
	defer span.End()

	database := m.client.Database(m.databaseName)
	collection := database.Collection(pinAttemptsCollection)

	_, err := collection.DeleteOne(ctx, bson.M{"_id": userID})
	if err != nil {
		return otel.RecordOutcome(err, span)
	}
	return nil
}
