package contract

// Baseline is baseline.json, the artifact of Stage −1 (protocol §5 Make It Testable; CB-05 in §7): the baseline that
// the run established on the base before launching any role. Mirrors contracts/baseline.schema.json. If the baseline
// could not be established, the file does not exist and the stage escalated prerequisite_failure.
type Baseline struct {
	SchemaVersion  int      `json:"schema_version"`
	SliceID        string   `json:"slice_id"`
	Base           string   `json:"base"`
	Command        string   `json:"command"`
	Image          string   `json:"image"`
	Tests          int      `json:"tests"`
	PreexistingRed []string `json:"preexisting_red"`
	Cached         bool     `json:"cached"`
	// ImageDigest is the digest of the tests image (sha256:...), or "" when the image has no digest.
	ImageDigest string `json:"image_digest"`
	// Reruns is how many times baseline_command ran on the base to establish this file (at least 1).
	Reruns int `json:"reruns"`
	// Stable is true when every rerun gave the same sorted (id, status) set.
	Stable bool `json:"stable"`
}
