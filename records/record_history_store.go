package records

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/juicebox-systems/juicebox-software-realm/otel"
	"github.com/juicebox-systems/juicebox-software-realm/types"
	"go.mongodb.org/mongo-driver/bson"
)

// RecordHistoryEntry is a backup of a single successful user record write.
type RecordHistoryEntry struct {
	UserRecordID UserRecordID `bson:"userRecordId"`
	RealmID      string       `bson:"realmId"`
	ArchivedAt   time.Time    `bson:"archivedAt"`
	// The stored version before the write, nil if the record was created.
	PreviousVersion *int64 `bson:"previousVersion"`
	// The stored version the write was expected to produce.
	NewVersion int64 `bson:"newVersion"`
	// Whether the write changed the registration, i.e. the before and after
	// registeredVersionHash differ.
	VersionChanged bool `bson:"versionChanged"`
	// The raw stored record as read before the write (still encrypted), nil if
	// the record was created. It can be written back to restore that state.
	Record bson.M `bson:"record"`
	// The non-secret parts of the record before and after the write.
	Before RecordStateSummary `bson:"before"`
	After  RecordStateSummary `bson:"after"`
}

// RecordStateSummary holds only the non-secret fields of a UserRecord.
type RecordStateSummary struct {
	RegistrationState string `bson:"registrationState"`
	// Hex sha256 of Registered.Version, nil unless registered. Stored as a
	// string so it can be pasted into queries from any Mongo console.
	RegisteredVersionHash *string `bson:"registeredVersionHash"`
	GuessCount            *int32  `bson:"guessCount"`
	NumGuesses            *int32  `bson:"numGuesses"`
}

// RecordHistoryStore keeps backups of user record writes.
type RecordHistoryStore interface {
	Insert(ctx context.Context, entry RecordHistoryEntry) error
}

func NewRecordHistoryStore(ctx context.Context, provider types.ProviderName, _ types.ProviderOptions, realmID types.RealmID) (RecordHistoryStore, error) {
	ctx, span := otel.StartSpan(ctx, "NewRecordHistoryStore")
	defer span.End()

	if !strings.EqualFold(os.Getenv("RECORD_HISTORY_ENABLED"), "true") {
		return nil, nil
	}

	switch provider {
	case types.Mongo:
		store, err := NewMongoRecordHistoryStore(ctx, realmID)
		if err != nil {
			return nil, err
		}
		return store, nil
	}

	return nil, nil
}
