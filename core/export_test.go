package core

import "time"

// MarkInterruptedRuns runs what Start does first; for tests.
func MarkInterruptedRuns(c *Core) { c.markInterruptedRuns() }

// SetServiceIntervals shortens the services' health checks for a test.
func SetServiceIntervals(check, retry time.Duration) (restore func()) {
	c, r := serviceCheckEvery, serviceRetryEvery
	serviceCheckEvery, serviceRetryEvery = check, retry
	return func() { serviceCheckEvery, serviceRetryEvery = c, r }
}
