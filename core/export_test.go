package core

import "time"

// MarkInterruptedRuns runs what Start does first; for tests.
func MarkInterruptedRuns(c *Core) { c.markInterruptedRuns() }

// SetServiceIntervals shortens the services' health checks for a test.
func SetServiceIntervals(check, retry time.Duration) (restore func()) {
	c, r := serviceCheckEvery.Load(), serviceRetryEvery.Load()
	serviceCheckEvery.Store(int64(check))
	serviceRetryEvery.Store(int64(retry))
	return func() { serviceCheckEvery.Store(c); serviceRetryEvery.Store(r) }
}

// EgressGranted is the egress grant check.
var EgressGranted = egressGranted

// AllowLoopbackEgress lets a test's local servers be egress targets.
func AllowLoopbackEgress() (restore func()) {
	egressAllowLoopback = true
	return func() { egressAllowLoopback = false }
}
