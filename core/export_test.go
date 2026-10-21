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

// EgressGranted is the egress grant check.
var EgressGranted = egressGranted

// AllowLoopbackEgress lets a test's local servers be egress targets.
func AllowLoopbackEgress() (restore func()) {
	egressAllowLoopback = true
	return func() { egressAllowLoopback = false }
}
