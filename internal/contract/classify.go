package contract

// Categories of the unexamined changed lines that the receipt records as a note and that do not, by themselves, make the
// verdict INCONCLUSIVE (docs/COVERAGE_NOTES.md). The core knows the names; what falls in each one is the engine's call.
const (
	NoteDefensiveHandler = "defensive_handler"
	NoteDeclarativeOnly  = "declarative_only"
)

// IsNoteCategory says whether c is a category of coverage note.
func IsNoteCategory(c string) bool { return c == NoteDefensiveHandler || c == NoteDeclarativeOnly }

// LineNote is a set of changed lines of one category that no engine examined and no test executed: the receipt says so
// and does not block on them. Lines are sorted and unique.
type LineNote struct {
	Category string `json:"category"`
	Lines    []int  `json:"lines"`
}

// ClassifyBudget is the budget of operation: classify_lines: the unexamined changed lines to classify, by file.
type ClassifyBudget struct {
	Lines map[string][]int `json:"lines"`
}

// ClassifyResponse is the response to operation: classify_lines (engine-classify-lines-response.schema.json): per
// requested file, the notes among the lines it was asked about. A line that is in no note stays unexamined.
type ClassifyResponse struct {
	SchemaVersion int            `json:"schema_version"`
	Files         []ClassifyFile `json:"files"`
	Error         *string        `json:"error"`
}

type ClassifyFile struct {
	File  string     `json:"file"`
	Notes []LineNote `json:"notes"`
}
