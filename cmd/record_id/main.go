// Command record_id prints the id a user's record is stored under, which is the
// `_id` in the userRecords collection and the `userRecordId` in the
// userRecordHistory collection. It doesn't depend on the realm, so it's the
// same in every realm.
//
// The id is derived from the tenant and the user's sub. The sub can be given
// directly, or derived from the user's login the way service-juicebox does,
// with the salt read from the USER_ID_SALT environment variable.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/juicebox-systems/juicebox-software-realm/records"
)

func main() {
	tenant := flag.String(
		"tenant",
		"",
		"The tenant name, i.e. service-juicebox's REALM_TENANT_NAME and the 'iss' claim of the user's realm JWT.",
	)
	sub := flag.String(
		"sub",
		"",
		"The user id, i.e. the 'sub' claim of the user's realm JWT. Alternatively use -provider and -provider-id.",
	)
	provider := flag.String(
		"provider",
		"",
		"The user's login provider, google or apple. Requires -provider-id and USER_ID_SALT.",
	)
	providerID := flag.String(
		"provider-id",
		"",
		"The provider_id of the user's account with that provider.",
	)
	testnet := flag.Bool(
		"testnet",
		false,
		"The user's wallet is a testnet one.",
	)
	flag.Parse()

	if *tenant == "" {
		fail(1, "-tenant is required")
	}

	switch {
	case *sub != "" && (*provider != "" || *providerID != ""):
		fail(1, "use either -sub or -provider and -provider-id, not both")
	case *sub == "":
		if *provider != "google" && *provider != "apple" {
			fail(1, "-provider must be google or apple, or use -sub")
		}
		if *providerID == "" {
			fail(1, "-provider-id is required with -provider")
		}
		salt := os.Getenv("USER_ID_SALT")
		if salt == "" {
			fail(1, "USER_ID_SALT must be set to service-juicebox's USER_ID_SALT")
		}
		derived := userSub(salt, *provider, *providerID, *testnet)
		sub = &derived
	}

	recordID, err := records.CreateUserRecordID(*tenant, *sub)
	if err != nil {
		fail(2, err.Error())
	}

	fmt.Println(recordID)
}

// Mirrors getIdFromMetadata in service-juicebox's juicebox.service.ts, which
// computes the sub of the user's realm JWTs.
func userSub(salt string, provider string, providerID string, testnet bool) string {
	extraData := ""
	if testnet {
		extraData = ":isTestnet"
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%s%s", salt, provider, providerID, extraData)))
	return hex.EncodeToString(hash[:])
}

func fail(code int, message string) {
	fmt.Fprintf(os.Stderr, "%s.\n\n", message)
	flag.Usage()
	os.Exit(code)
}
