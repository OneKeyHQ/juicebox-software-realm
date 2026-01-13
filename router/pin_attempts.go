package router

import (
	"os"
	"strings"
	"time"

	"github.com/juicebox-systems/juicebox-software-realm/records"
)

const pinAttemptLockoutCount = 7

var pinAttemptDelays = pinAttemptDelaysFromEnv()

func pinAttemptDelaysFromEnv() []time.Duration {
	if strings.EqualFold(os.Getenv("PIN_ATTEMPT_DELAY_MODE"), "test") {
		return []time.Duration{
			0,
			time.Minute,
			2 * time.Minute,
			3 * time.Minute,
			4 * time.Minute,
			5 * time.Minute,
		}
	}

	return []time.Duration{
		0,
		30 * time.Second,
		60 * time.Second,
		120 * time.Second,
		300 * time.Second,
		300 * time.Second,
	}
}

func updatePinAttempt(attempt records.PinAttempt, now time.Time) records.PinAttempt {
	attempt.TryCount++
	delay, locked := pinAttemptDelay(attempt.TryCount)
	if locked {
		attempt.RetryAt = time.Time{}
	} else {
		attempt.RetryAt = now.Add(delay)
	}
	return attempt
}

func shouldRateLimitAttempt(attempt records.PinAttempt, now time.Time) bool {
	if attempt.TryCount >= pinAttemptLockoutCount {
		return true
	}
	if !attempt.RetryAt.IsZero() && now.Before(attempt.RetryAt) {
		return true
	}
	return false
}

func pinAttemptDelay(tryCount int) (time.Duration, bool) {
	if tryCount >= pinAttemptLockoutCount {
		return 0, true
	}
	if tryCount <= 0 {
		return 0, false
	}
	if tryCount > len(pinAttemptDelays) {
		return pinAttemptDelays[len(pinAttemptDelays)-1], false
	}
	return pinAttemptDelays[tryCount-1], false
}
