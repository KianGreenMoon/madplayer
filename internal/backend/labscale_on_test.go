//go:build race

package backend

// labTimeoutScale stretches the lab's speed-bound budgets under the race
// detector, where the loopback yggdrasil overlay's per-chunk crypto runs
// many times slower: measured 2026-09-07 on this laptop, the baseline
// scenario's 3 MiB swarm fetch took 28.8 s under -race against 3 s without
// — one second inside fetchUntil's 30 s per-attempt budget, and over it
// when the rest of the suite runs alongside. madshare's federation tests
// carry the same 8× (federation/racescale_on_test.go). Only how long an
// attempt may take is scaled; the clock-bound windows (fetchNever's, the
// vouch clocks) are not, since the race detector does not slow a calendar.
const labTimeoutScale = 8
