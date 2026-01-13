package records

import (
	"context"
	"time"

	"github.com/juicebox-systems/juicebox-software-realm/otel"
	"github.com/juicebox-systems/juicebox-software-realm/types"
)

type PinAttempt struct {
	TryCount int       `bson:"try_count"`
	RetryAt  time.Time `bson:"retry_at"`
}

type PinAttemptStore interface {
	Get(ctx context.Context, userID string) (PinAttempt, error)
	Upsert(ctx context.Context, userID string, attempt PinAttempt) error
	Delete(ctx context.Context, userID string) error
}

func NewPinAttemptStore(ctx context.Context, provider types.ProviderName, _ types.ProviderOptions, realmID types.RealmID) (PinAttemptStore, error) {
	ctx, span := otel.StartSpan(ctx, "NewPinAttemptStore")
	defer span.End()

	switch provider {
	case types.Mongo:
		return NewMongoPinAttemptStore(ctx, realmID)
	}

	return nil, nil
}
