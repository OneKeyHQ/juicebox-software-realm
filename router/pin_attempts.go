package router

import (
	"os"
	"strings"
	"time"

	"github.com/juicebox-systems/juicebox-software-realm/records"
)

var pinAttemptDelays = pinAttemptDelaysFromEnv()

func pinAttemptDelaysFromEnv() []time.Duration {
	if strings.EqualFold(os.Getenv("PIN_ATTEMPT_DELAY_MODE"), "test") {
		return []time.Duration{
			0,
			30 * time.Second,
			60 * time.Second,
			90 * time.Second,
			120 * time.Second,
			150 * time.Second,
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

func updatePinAttempt(attempt records.PinAttempt, guessCount uint16, now time.Time) records.PinAttempt {
	delay := pinAttemptDelay(guessCount)
	if delay == 0 {
		attempt.RetryAt = time.Time{}
	} else {
		attempt.RetryAt = now.Add(delay)
	}
	return attempt
}

func shouldRateLimitAttempt(attempt records.PinAttempt, guessCount, numGuess uint16, now time.Time) bool {
	if numGuess > 0 && guessCount >= numGuess {
		return true
	}
	if !attempt.RetryAt.IsZero() && now.Before(attempt.RetryAt) {
		return true
	}
	return false
}

func pinAttemptDelay(guessCount uint16) time.Duration {
	if guessCount <= 1 {
		return 0
	}

	index := int(guessCount) - 1
	if index >= len(pinAttemptDelays) {
		index = len(pinAttemptDelays) - 1
	}
	return pinAttemptDelays[index]
}
