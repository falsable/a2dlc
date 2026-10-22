// Answers of the operations this engine does not perform. Each body is in the shape of its response schema
// (contracts/engine-*-response.schema.json), so the core reads the error instead of failing to decode the response.
// tests/responses.test.mjs checks every body against its schema.

// The error bodies, by operation. Every field of each schema is present; the lists are empty.
export const ERROR_BODIES = {
  mutate: (error) => ({ schema_version: 1, mutants: [], not_generated: [], pruned: [], error }),
  complexity: (error) => ({ schema_version: 1, files: [], error }),
  lemmas: (error) => ({ schema_version: 1, lemmas: [], error }),
  golden_record: (error) => ({ schema_version: 1, cases: [], diffs: [], error }),
  golden_replay: (error) => ({ schema_version: 1, cases: [], diffs: [], error }),
  capture_traces: (error) => ({
    schema_version: 1, runner: null, test_exit: null, traces: [], skipped: [], error,
  }),
};

// Operations whose response schema has no error field (fuzz: additionalProperties is false and an operation-wide
// error cannot be written). Writing findings: [] with runs 0 would read as "fuzzed, nothing found", so no response is
// written and the process exits with 2: the core reports the engine as having failed to answer.
export const NO_ERROR_FIELD = new Set(['fuzz']);
