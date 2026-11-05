// Package analytics holds the indexer's domain logic as pure functions over
// typed inputs: trade accounting (PnL, round trips, volume breakdowns),
// portfolio valuation and history reconstruction, behavioural tag
// heuristics, label rules, alert assembly and market math. It performs no
// I/O; the query package loads its inputs and the api package serves its
// outputs, so every rule here is unit-testable in isolation.
package analytics
