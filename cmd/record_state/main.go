// Command record_state prints the non-secret state of a user's record in a
// realm, decrypting it the same way the realm does. It only reads, from the
// primary, to confirm a realm's current registration before and after a
// manual restore.
//
// It reads the same environment as jb-sw-realm with the mongo provider:
// MONGO_URL, the Volcengine KMS settings and, by default, REALM_ID. With
// -env-file it loads them from the realm's systemd EnvironmentFile instead, so
// they never pass through a shell.
package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/juicebox-systems/juicebox-software-realm/records"
	"github.com/juicebox-systems/juicebox-software-realm/types"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type recordState struct {
	RealmID               string  `json:"realmId"`
	Database              string  `json:"database"`
	UserRecordID          string  `json:"userRecordId"`
	Encrypted             bool    `json:"encrypted"`
	Version               *int64  `json:"version"`
	RegistrationState     string  `json:"registrationState"`
	RegisteredVersionHash *string `json:"registeredVersionHash"`
	GuessCount            *int32  `json:"guessCount"`
	NumGuesses            *int32  `json:"numGuesses"`
}

func main() {
	idString := flag.String(
		"id",
		"",
		"The 16-byte hex id of the realm to read from. (default $REALM_ID)",
	)
	recordID := flag.String(
		"record",
		"",
		"The user's record id, as printed by record_id.",
	)
	envFile := flag.String(
		"env-file",
		"",
		"Load the environment from this file first, e.g. the realm's systemd EnvironmentFile.",
	)
	allowUnencrypted := flag.Bool(
		"allow-unencrypted",
		false,
		`Also print plaintext records from before KMS encryption. Their realm can't
be verified through KMS, so first make sure MONGO_URL is this realm's database.`,
	)
	flag.Parse()

	if *envFile != "" {
		if err := loadEnvFile(*envFile); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to load %s: %s, exiting...\n", *envFile, err)
			os.Exit(1)
		}
	}

	if envIDString := os.Getenv("REALM_ID"); envIDString != "" && *idString == "" {
		idString = &envIDString
	}

	if *idString == "" || *recordID == "" {
		fmt.Fprintf(os.Stderr, "Both -id (or REALM_ID) and -record are required.\n\n")
		flag.Usage()
		os.Exit(1)
	}

	parsedID, err := hex.DecodeString(*idString)
	if err != nil || len(parsedID) != 16 {
		fmt.Fprintf(os.Stderr, "Invalid realm id %q, exiting...\n", *idString)
		os.Exit(2)
	}
	realmID := types.RealmID(parsedID)

	ctx := context.Background()

	inspector, err := records.NewMongoRecordInspector(ctx, realmID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to record store: %s, exiting...\n", redactCredentials(err.Error(), os.Getenv("MONGO_URL")))
		os.Exit(3)
	}

	record, readRecord, err := inspector.GetRecord(ctx, records.UserRecordID(*recordID))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read record (wrong realm id or KMS settings?): %s, exiting...\n", redactCredentials(err.Error(), os.Getenv("MONGO_URL")))
		os.Exit(4)
	}
	if readRecord == nil {
		fmt.Fprintf(os.Stderr, "No record %s in database %s, exiting...\n", *recordID, inspector.DatabaseName)
		os.Exit(5)
	}

	encrypted := records.IsEncryptedRecord(readRecord)
	if !encrypted && !*allowUnencrypted {
		fmt.Fprintf(os.Stderr, "Record %s in database %s is a plaintext record, so it can't be verified to belong to realm %s. Make sure MONGO_URL is this realm's database, then rerun with -allow-unencrypted. Exiting...\n", *recordID, inspector.DatabaseName, realmID.String())
		os.Exit(6)
	}

	summary := records.SummarizeRecordState(record)
	state := recordState{
		RealmID:               realmID.String(),
		Database:              inspector.DatabaseName,
		UserRecordID:          *recordID,
		Encrypted:             encrypted,
		RegistrationState:     summary.RegistrationState,
		RegisteredVersionHash: summary.RegisteredVersionHash,
		GuessCount:            summary.GuessCount,
		NumGuesses:            summary.NumGuesses,
	}
	if raw, ok := readRecord.(primitive.M); ok {
		if version, ok := raw["version"].(int64); ok {
			state.Version = &version
		}
	}

	output, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s, exiting...\n", err)
		os.Exit(7)
	}
	fmt.Println(string(output))
}

// The inspector never passes on errors from parsing MONGO_URL, the ones that
// quote it. This is a second line of defense: rather than parsing MONGO_URL,
// take everything between "://" and the last "@" as the credentials, and
// remove them and their parts wherever they appear, as is or quoted.
func redactCredentials(message string, mongoURL string) string {
	start := strings.Index(mongoURL, "://")
	end := strings.LastIndex(mongoURL, "@")
	if start < 0 || end <= start+3 {
		return message
	}
	userInfo := mongoURL[start+3 : end]
	secrets := []string{userInfo}
	if user, password, ok := strings.Cut(userInfo, ":"); ok {
		secrets = append(secrets, password, user)
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		quoted := strconv.Quote(secret)
		message = strings.ReplaceAll(message, quoted[1:len(quoted)-1], "<redacted>")
		message = strings.ReplaceAll(message, secret, "<redacted>")
	}
	return message
}

// Sets the variables in a systemd EnvironmentFile: KEY=VALUE lines, with "#"
// or ";" comments and optionally quoted values. Errors never quote the file,
// which holds secrets.
func loadEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") || strings.HasPrefix(text, ";") {
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return fmt.Errorf("line %d isn't KEY=VALUE", line)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
	}
	return scanner.Err()
}
