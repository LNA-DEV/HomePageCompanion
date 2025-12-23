package interactions

import (
	"errors"
	"log"
	"math"
	"time"
)

// RetryConfig holds configuration for retry with exponential backoff
type RetryConfig struct {
	MaxRetries     int
	InitialDelay   time.Duration
	MaxDelay       time.Duration
	BackoffFactor  float64
}

// DefaultRetryConfig returns sensible defaults for rate limit handling
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:    10,
		InitialDelay:  time.Second,
		MaxDelay:      5 * time.Minute,
		BackoffFactor: 2.0,
	}
}

// RetryWithBackoff executes a function with exponential backoff retry on rate limit errors
func RetryWithBackoff[T any](config RetryConfig, operation func() (T, error)) (T, error) {
	var result T
	var lastErr error

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		result, lastErr = operation()

		if lastErr == nil {
			return result, nil
		}

		// Only retry on rate limit errors
		if !errors.Is(lastErr, ErrRateLimited) {
			return result, lastErr
		}

		if attempt == config.MaxRetries {
			break
		}

		// Calculate delay with exponential backoff
		delay := time.Duration(float64(config.InitialDelay) * math.Pow(config.BackoffFactor, float64(attempt)))
		if delay > config.MaxDelay {
			delay = config.MaxDelay
		}

		log.Printf("Rate limited, retrying in %v (attempt %d/%d)", delay, attempt+1, config.MaxRetries)
		time.Sleep(delay)
	}

	return result, lastErr
}
