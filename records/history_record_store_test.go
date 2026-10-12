package records

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/juicebox-systems/juicebox-software-realm/types"
	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type fakeRecordStore struct {
	record    UserRecord
	raw       interface{}
	getErr    error
	writeErr  error
	writes    int
	written   UserRecord
	writtenID UserRecordID
	writtenRR interface{}
}

func (f *fakeRecordStore) GetRecord(_ context.Context, _ UserRecordID) (UserRecord, interface{}, error) {
	return f.record, f.raw, f.getErr
}

func (f *fakeRecordStore) WriteRecord(_ context.Context, recordID UserRecordID, record UserRecord, readRecord interface{}) error {
	f.writes++
	f.writtenID = recordID
	f.written = record
	f.writtenRR = readRecord
	return f.writeErr
}

type fakeHistoryStore struct {
	entries chan RecordHistoryEntry
	block   chan struct{}
	err     error
	panics  bool
}

func newFakeHistoryStore() *fakeHistoryStore {
	return &fakeHistoryStore{entries: make(chan RecordHistoryEntry, 100)}
}

func (f *fakeHistoryStore) Insert(ctx context.Context, entry RecordHistoryEntry) error {
	if f.block != nil {
		<-f.block
	}
	if f.panics {
		panic("boom")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	f.entries <- entry
	return f.err
}

func (f *fakeHistoryStore) next(t *testing.T) RecordHistoryEntry {
	select {
	case entry := <-f.entries:
		return entry
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for history entry")
		return RecordHistoryEntry{}
	}
}

func (f *fakeHistoryStore) assertEmpty(t *testing.T) {
	select {
	case entry := <-f.entries:
		t.Fatalf("unexpected history entry: %+v", entry)
	case <-time.After(50 * time.Millisecond):
	}
}

func waitForIdle(t *testing.T, store *HistoryRecordStore) {
	assert.Eventually(t, func() bool { return len(store.inFlight) == 0 }, time.Second, time.Millisecond)
}

var testRealmID = types.RealmID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}

var testVersion0 = types.RegistrationVersion{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}
var testVersion1 = types.RegistrationVersion{0x11}

func registeredRecord(version types.RegistrationVersion, guessCount uint16) UserRecord {
	return UserRecord{
		RegistrationState: Registered{
			Version:    version,
			GuessCount: guessCount,
			Policy:     types.Policy{NumGuesses: 10},
		},
	}
}

func registeredSummary(version types.RegistrationVersion, guessCount int32) RecordStateSummary {
	hash := sha256.Sum256(version[:])
	hashHex := hex.EncodeToString(hash[:])
	numGuesses := int32(10)
	return RecordStateSummary{
		RegistrationState:     "Registered",
		RegisteredVersionHash: &hashHex,
		GuessCount:            &guessCount,
		NumGuesses:            &numGuesses,
	}
}

func mongoReadRecord(version int64) primitive.M {
	return primitive.M{
		"_id":                  "id",
		"serializedUserRecord": primitive.Binary{Data: []byte{1, 2, 3}},
		"encryptedDataKey":     "key",
		"encryptionNonce":      primitive.Binary{Data: []byte{4, 5, 6}},
		"encryptionVersion":    int32(1),
		"version":              version,
	}
}

func TestHistoryRecordStoreGetRecord(t *testing.T) {
	raw := mongoReadRecord(3)
	inner := &fakeRecordStore{record: registeredRecord(testVersion0, 0), raw: raw}
	store := NewHistoryRecordStore(inner, newFakeHistoryStore(), testRealmID)

	record, readRecord, err := store.GetRecord(context.Background(), "id")
	assert.NoError(t, err)
	assert.Equal(t, registeredRecord(testVersion0, 0), record)
	assert.Equal(t, historyReadRecord{raw: raw, record: record}, readRecord)

	// Nothing stored yet: the nil read record is passed through.
	inner = &fakeRecordStore{record: DefaultUserRecord()}
	store = NewHistoryRecordStore(inner, newFakeHistoryStore(), testRealmID)
	record, readRecord, err = store.GetRecord(context.Background(), "id")
	assert.NoError(t, err)
	assert.Equal(t, DefaultUserRecord(), record)
	assert.Nil(t, readRecord)

	// Errors are passed through untouched.
	getErr := errors.New("read failed")
	inner = &fakeRecordStore{record: DefaultUserRecord(), raw: raw, getErr: getErr}
	store = NewHistoryRecordStore(inner, newFakeHistoryStore(), testRealmID)
	record, readRecord, err = store.GetRecord(context.Background(), "id")
	assert.Same(t, getErr, err)
	assert.Equal(t, DefaultUserRecord(), record)
	assert.Equal(t, raw, readRecord)
}

func TestHistoryRecordStoreWritesThroughUnchanged(t *testing.T) {
	raw := mongoReadRecord(5)
	inner := &fakeRecordStore{record: registeredRecord(testVersion0, 0), raw: raw}
	history := newFakeHistoryStore()
	store := NewHistoryRecordStore(inner, history, testRealmID)

	_, readRecord, err := store.GetRecord(context.Background(), "id")
	assert.NoError(t, err)
	err = store.WriteRecord(context.Background(), "id", registeredRecord(testVersion1, 0), readRecord)
	assert.NoError(t, err)

	// The wrapped store must get the exact raw record it returned.
	assert.Equal(t, 1, inner.writes)
	assert.Equal(t, UserRecordID("id"), inner.writtenID)
	assert.Equal(t, registeredRecord(testVersion1, 0), inner.written)
	assert.IsType(t, primitive.M{}, inner.writtenRR)
	assert.Equal(t, reflect.ValueOf(raw).Pointer(), reflect.ValueOf(inner.writtenRR).Pointer())

	expectedRecord := bson.M{}
	for key, value := range raw {
		expectedRecord[key] = value
	}
	// The backup must hold a copy, unaffected by later changes to the read record.
	raw["version"] = int64(99)

	entry := history.next(t)
	assert.Equal(t, UserRecordID("id"), entry.UserRecordID)
	assert.Equal(t, testRealmID.String(), entry.RealmID)
	assert.WithinDuration(t, time.Now(), entry.ArchivedAt, time.Second)
	assert.Equal(t, int64(5), *entry.PreviousVersion)
	assert.Equal(t, int64(6), entry.NewVersion)
	assert.True(t, entry.VersionChanged)
	assert.Equal(t, expectedRecord, entry.Record)
	assert.Equal(t, registeredSummary(testVersion0, 0), entry.Before)
	assert.Equal(t, registeredSummary(testVersion1, 0), entry.After)
}

func TestHistoryRecordStoreWithMemoryStore(t *testing.T) {
	history := newFakeHistoryStore()
	store := NewHistoryRecordStore(NewMemoryRecordStore(), history, testRealmID)
	ctx := context.Background()

	// Register.
	record, readRecord, err := store.GetRecord(ctx, "id")
	assert.NoError(t, err)
	assert.Equal(t, DefaultUserRecord(), record)
	assert.NoError(t, store.WriteRecord(ctx, "id", registeredRecord(testVersion0, 0), readRecord))

	entry := history.next(t)
	assert.Nil(t, entry.PreviousVersion)
	assert.Equal(t, int64(0), entry.NewVersion)
	assert.Nil(t, entry.Record)
	assert.True(t, entry.VersionChanged)
	assert.Equal(t, RecordStateSummary{RegistrationState: "NotRegistered"}, entry.Before)
	assert.Equal(t, registeredSummary(testVersion0, 0), entry.After)

	// Use a guess.
	record, readRecord, err = store.GetRecord(ctx, "id")
	assert.NoError(t, err)
	assert.Equal(t, registeredRecord(testVersion0, 0), record)
	assert.NoError(t, store.WriteRecord(ctx, "id", registeredRecord(testVersion0, 1), readRecord))

	entry = history.next(t)
	assert.False(t, entry.VersionChanged)
	assert.Equal(t, registeredSummary(testVersion0, 0), entry.Before)
	assert.Equal(t, registeredSummary(testVersion0, 1), entry.After)

	// A stale read record is still rejected by the wrapped store, with no backup.
	err = store.WriteRecord(ctx, "id", registeredRecord(testVersion1, 0), readRecord)
	assert.Error(t, err)
	history.assertEmpty(t)

	record, _, err = store.GetRecord(ctx, "id")
	assert.NoError(t, err)
	assert.Equal(t, registeredRecord(testVersion0, 1), record)
}

func TestHistoryRecordStoreUnwrappedReadRecord(t *testing.T) {
	inner := &fakeRecordStore{}
	history := newFakeHistoryStore()
	store := NewHistoryRecordStore(inner, history, testRealmID)

	raw := mongoReadRecord(2)
	err := store.WriteRecord(context.Background(), "id", UserRecord{RegistrationState: NoGuesses{}}, raw)
	assert.NoError(t, err)
	assert.Equal(t, reflect.ValueOf(raw).Pointer(), reflect.ValueOf(inner.writtenRR).Pointer())

	entry := history.next(t)
	assert.Equal(t, int64(2), *entry.PreviousVersion)
	assert.False(t, entry.VersionChanged)
	assert.Equal(t, RecordStateSummary{RegistrationState: "Unknown"}, entry.Before)
	assert.Equal(t, RecordStateSummary{RegistrationState: "NoGuesses"}, entry.After)
}

func TestHistoryRecordStoreWriteErrorSkipsBackup(t *testing.T) {
	writeErr := errors.New("write failed")
	inner := &fakeRecordStore{writeErr: writeErr}
	history := newFakeHistoryStore()
	store := NewHistoryRecordStore(inner, history, testRealmID)

	err := store.WriteRecord(context.Background(), "id", registeredRecord(testVersion0, 0), nil)
	assert.Same(t, writeErr, err)
	assert.Equal(t, 1, inner.writes)
	history.assertEmpty(t)
}

func TestHistoryRecordStoreBackupOutlivesRequest(t *testing.T) {
	history := newFakeHistoryStore()
	store := NewHistoryRecordStore(&fakeRecordStore{}, history, testRealmID)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.NoError(t, store.WriteRecord(ctx, "id", registeredRecord(testVersion0, 0), nil))
	history.next(t)
}

func TestHistoryRecordStoreBackupFailuresAreIgnored(t *testing.T) {
	inner := &fakeRecordStore{}
	history := newFakeHistoryStore()
	history.err = errors.New("insert failed")
	store := NewHistoryRecordStore(inner, history, testRealmID)

	err := store.WriteRecord(context.Background(), "id", registeredRecord(testVersion0, 0), nil)
	assert.NoError(t, err)
	history.next(t)
	waitForIdle(t, store)

	// A panic in the background must not take down the process.
	inner = &fakeRecordStore{}
	history = newFakeHistoryStore()
	history.panics = true
	store = NewHistoryRecordStore(inner, history, testRealmID)

	err = store.WriteRecord(context.Background(), "id", registeredRecord(testVersion0, 0), nil)
	assert.NoError(t, err)
	waitForIdle(t, store)
	assert.Equal(t, 1, inner.writes)
}

func TestHistoryRecordStoreDropsBackupsWhenBusy(t *testing.T) {
	inner := &fakeRecordStore{}
	history := newFakeHistoryStore()
	history.block = make(chan struct{})
	store := newHistoryRecordStore(inner, history, testRealmID, 2)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 5; i++ {
			assert.NoError(t, store.WriteRecord(context.Background(), "id", registeredRecord(testVersion0, 0), nil))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WriteRecord blocked on pending backups")
	}
	assert.Equal(t, 5, inner.writes)
	assert.Equal(t, int64(3), store.dropped.Load())

	// Drops are reported from the background once backups resume.
	close(history.block)
	history.next(t)
	history.next(t)
	history.assertEmpty(t)
	waitForIdle(t, store)
	assert.Equal(t, int64(0), store.dropped.Load())
}

func TestRecordHistoryClientOptions(t *testing.T) {
	// A valid MONGO_URL whose minPoolSize exceeds the history pool's maximum.
	opts := recordHistoryClientOptions("mongodb://localhost:27017/db?minPoolSize=10&maxPoolSize=100")
	assert.NoError(t, opts.Validate())
	assert.Equal(t, uint64(0), *opts.MinPoolSize)
	assert.Equal(t, uint64(4), *opts.MaxPoolSize)
}

func TestSummarizeRecordState(t *testing.T) {
	summary := SummarizeRecordState(registeredRecord(testVersion0, 3))
	// Computed independently: sha256 of the 16 version bytes, as hex.
	assert.Equal(t, "6ea7457ea08fa6904b3ec5afbcd851b5b1d279c3bb097c919bf65df6334a4308", *summary.RegisteredVersionHash)
	assert.Equal(t, int32(3), *summary.GuessCount)
	assert.Equal(t, int32(10), *summary.NumGuesses)

	assert.Equal(t, RecordStateSummary{RegistrationState: "NoGuesses"}, SummarizeRecordState(UserRecord{RegistrationState: NoGuesses{}}))
	assert.Equal(t, RecordStateSummary{RegistrationState: "NotRegistered"}, SummarizeRecordState(DefaultUserRecord()))
	assert.Equal(t, RecordStateSummary{RegistrationState: "Unknown"}, SummarizeRecordState(UserRecord{}))
}
