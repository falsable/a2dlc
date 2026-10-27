package contract

// Level0 is the value of Verdict.Level for the static level (a2dlc check --level 0). A receipt without Level is level 1.
const Level0 = 0

// Status of the import reading of a level 0 receipt (verdict.schema.json#/properties/static/properties/imports).
const (
	ImportsRan           = "ran"
	ImportsUnavailable   = "unavailable"
	ImportsNotApplicable = "not_applicable"
)

// StaticReport is what level 0 says about a diff without running anything of the repo. The import reading is direct only:
// a source reached through an unchanged module shows as not imported by the diff's tests.
type StaticReport struct {
	ChangedSources         []string        `json:"changed_sources"`
	ChangedTests           []string        `json:"changed_tests"`
	BringsTests            bool            `json:"brings_tests"`
	Tests                  []StaticTest    `json:"tests"`
	Imports                StaticImports   `json:"imports"`
	NotImportedByDiffTests []string        `json:"not_imported_by_diff_tests"`
	ProtectedPaths         []ProtectedPath `json:"protected_paths"`
}

// StaticTest is the static listing of one changed test file. New is nil when the base's file could not be listed.
type StaticTest struct {
	File     string `json:"file"`
	Listable bool   `json:"listable"`
	Reason   string `json:"reason,omitempty"`
	Count    *int   `json:"count,omitempty"`
	New      *int   `json:"new,omitempty"`
}

type StaticImports struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type ProtectedPath struct {
	File string `json:"file"`
	Glob string `json:"glob"`
}
