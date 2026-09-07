//go:build !race

package backend

// labTimeoutScale multiplies the lab's speed-bound budgets. Normal builds run
// at 1×; the race build (labscale_on_test.go) stretches them because the
// loopback overlay's crypto is many times slower under -race.
const labTimeoutScale = 1
