package contract

// ListedTest is a test function found in a file by reading its source: Class is the dotted class path ("" at module
// level) and Name the function name, without any parametrize suffix.
type ListedTest struct {
	Class string `json:"class"`
	Name  string `json:"name"`
}

// ListedFile is the static listing of one test file (operation: list_tests). A file that is not Listable says why and lists
// nothing; the core then runs the base for it.
type ListedFile struct {
	File     string       `json:"file"`
	Listable bool         `json:"listable"`
	Reason   string       `json:"reason,omitempty"`
	Tests    []ListedTest `json:"tests"`
}

// ListTestsResponse is the response to operation: list_tests (engine-list-tests-response.schema.json).
type ListTestsResponse struct {
	SchemaVersion int          `json:"schema_version"`
	Files         []ListedFile `json:"files"`
	Error         *string      `json:"error"`
}
