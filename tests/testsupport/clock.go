package testsupport

import "time"

func FixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}
