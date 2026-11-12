package pipeline

import "time"

// PollMax is pollMax for the external tests.
const PollMax = pollMax

// SetResultTimeout sets resultTimeout for a test and restores it after.
func SetResultTimeout(d time.Duration) (restore func()) {
	old := resultTimeout
	resultTimeout = d
	return func() { resultTimeout = old }
}
