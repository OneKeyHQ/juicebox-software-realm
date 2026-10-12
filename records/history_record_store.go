package records

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"sync/atomic"
	"time"

	"github.com/juicebox-systems/juicebox-software-realm/otel"
	"github.com/juicebox-systems/juicebox-software-realm/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.opentelemetry.io/otel/attribute"
)

const recordHistoryMaxInFlight = 64
const recordHistoryWriteTimeout = 5 * time.Second

// HistoryRecordStore wraps a RecordStore and, after every successful write,
// saves a backup of the change to a RecordHistoryStore.
//
// Backups are best effort and run in the background: they never block, fail,
// or otherwise change the behavior of the wrapped store.
type HistoryRecordStore struct {
	inner    RecordStore
	history  RecordHistoryStore
	realmID  string
	inFlight chan struct{}
	// Backups dropped since this was last logged. Logging happens in the
	// background so a dropped backup costs the request nothing.
	dropped atomic.Int64
}

// historyReadRecord carries the decoded record next to the raw one returned by
// the wrapped store, so a backup can describe the state before a write without
// calling KMS. The wrapped store only ever sees the raw record.
type historyReadRecord struct {
	raw    interface{}
	record UserRecord
}

func NewHistoryRecordStore(inner RecordStore, history RecordHistoryStore, realmID types.RealmID) *HistoryRecordStore {
	return newHistoryRecordStore(inner, history, realmID, recordHistoryMaxInFlight)
}

func newHistoryRecordStore(inner RecordStore, history RecordHistoryStore, realmID types.RealmID, maxInFlight int) *HistoryRecordStore {
	return &HistoryRecordStore{
		inner:    inner,
		history:  history,
		realmID:  realmID.String(),
		inFlight: make(chan struct{}, maxInFlight),
	}
}

func (h *HistoryRecordStore) GetRecord(ctx context.Context, recordID UserRecordID) (UserRecord, interface{}, error) {
	record, readRecord, err := h.inner.GetRecord(ctx, recordID)
	// A nil read record means nothing is stored yet, which the wrapped store
	// relies on when writing, so it's passed through as is.
	if err != nil || readRecord == nil {
		return record, readRecord, err
	}
	return record, historyReadRecord{raw: readRecord, record: record}, nil
}

func (h *HistoryRecordStore) WriteRecord(ctx context.Context, recordID UserRecordID, record UserRecord, readRecord interface{}) error {
	// Without a read record nothing was stored, otherwise the previous state
	// is unknown unless it was read through this store.
	before := UserRecord{}
	if readRecord == nil {
		before = DefaultUserRecord()
	}
	if wrapped, ok := readRecord.(historyReadRecord); ok {
		readRecord = wrapped.raw
		before = wrapped.record
	}

	err := h.inner.WriteRecord(ctx, recordID, record, readRecord)
	if err == nil {
		h.backup(ctx, recordID, before, record, readRecord)
	}
	return err
}

func (h *HistoryRecordStore) backup(ctx context.Context, recordID UserRecordID, before UserRecord, after UserRecord, readRecord interface{}) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("record history: panic backing up record %s: %v", recordID, r)
		}
	}()

	// Built before returning so the backup can't race with the caller.
	entry := newRecordHistoryEntry(recordID, h.realmID, before, after, readRecord, time.Now())

	// The request context is canceled once the response is sent, so detach
	// from it while keeping its trace.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordHistoryWriteTimeout)

	select {
	case h.inFlight <- struct{}{}:
	default:
		cancel()
		h.dropped.Add(1)
		otel.IncrementInt64Counter(ctx, "realm.record_history.count", attribute.String("outcome", "dropped"))
		return
	}

	go func() {
		defer func() { <-h.inFlight }()
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("record history: panic backing up record %s: %v", recordID, r)
			}
		}()
		defer func() {
			if dropped := h.dropped.Swap(0); dropped > 0 {
				log.Printf("record history: dropped %d backups because too many were pending", dropped)
			}
		}()

		if err := h.history.Insert(ctx, entry); err != nil {
			log.Printf("record history: failed to back up record %s: %v", recordID, err)
			otel.IncrementInt64Counter(ctx, "realm.record_history.count", attribute.String("outcome", "error"))
			return
		}
		otel.IncrementInt64Counter(ctx, "realm.record_history.count", attribute.String("outcome", "ok"))
	}()
}

func newRecordHistoryEntry(recordID UserRecordID, realmID string, before UserRecord, after UserRecord, readRecord interface{}, now time.Time) RecordHistoryEntry {
	entry := RecordHistoryEntry{
		UserRecordID: recordID,
		RealmID:      realmID,
		ArchivedAt:   now,
		Before:       SummarizeRecordState(before),
		After:        SummarizeRecordState(after),
	}
	entry.VersionChanged = !equalStringPointers(entry.Before.RegisteredVersionHash, entry.After.RegisteredVersionHash)

	// Mirrors how MongoRecordStore.WriteRecord derives the new version.
	if raw, ok := readRecord.(primitive.M); ok {
		entry.Record = make(bson.M, len(raw))
		for key, value := range raw {
			entry.Record[key] = value
		}
		if version, ok := raw[versionKey].(int64); ok {
			entry.PreviousVersion = &version
			entry.NewVersion = version + 1
		}
	}

	return entry
}

// SummarizeRecordState returns the non-secret parts of a UserRecord.
func SummarizeRecordState(record UserRecord) RecordStateSummary {
	switch state := record.RegistrationState.(type) {
	case Registered:
		versionHash := sha256.Sum256(state.Version[:])
		versionHashHex := hex.EncodeToString(versionHash[:])
		guessCount := int32(state.GuessCount)
		numGuesses := int32(state.Policy.NumGuesses)
		return RecordStateSummary{
			RegistrationState:     "Registered",
			RegisteredVersionHash: &versionHashHex,
			GuessCount:            &guessCount,
			NumGuesses:            &numGuesses,
		}
	case NoGuesses:
		return RecordStateSummary{RegistrationState: "NoGuesses"}
	case NotRegistered:
		return RecordStateSummary{RegistrationState: "NotRegistered"}
	}
	return RecordStateSummary{RegistrationState: "Unknown"}
}

func equalStringPointers(a *string, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
