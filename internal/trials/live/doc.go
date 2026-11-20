// Package live is the offline half of the Forever trials runner: the slot
// lease, the typed serve client, the GM allowlist, the ledger, the monotonic
// marks clock, the 1 Hz collector and the dry-run trial loop. Real setup and
// situation scripts arrive in later rounds; this round ships only the --dry
// scripted ones.
//
// The runner talks to a running `forever serve --dir <slot dir>` through the
// slot RPC client, writes one repeat folder per repeat (raw-ta.jsonl,
// samples.jsonl, marks.jsonl, setup.json), then calls the ported
// events.Normalise and score unchanged. A Forever run folder scores the same
// with Go and trials.py.
//
// File layout mirrors the Python trl/live package: slot.go (leases),
// client.go (typed serve wrappers), guard.go (GM allowlist), ledger.go
// (cleanup trail), marks.go (monotonic clock), collect.go (pump + sampler),
// run.go (RunTrial + dry scripts).
package live
