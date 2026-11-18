// Package parityfix pins the divergences the first review found between the Go port and the
// Python bench: row identity, float counts, null means, huge GUIDs, non-object JSONL lines and
// unhashable seq values. The expectations are the Python reference's output, printed by
// TRIALS_PYTHON_ROOT rules.evaluate / events.norm_guid and recorded here.
package parityfix
