<?php
// Answers of the operations this engine does not perform. Each body is in the shape of its response schema
// (contracts/engine-*-response.schema.json), so the core reads the error instead of failing to decode the response.
// tests/responses_test.php checks every body against its schema.

namespace A2dlc\PhpEngine;

final class Responses
{
    /** @return array<string, callable(string): array<string, mixed>> */
    public static function errorBodies(): array
    {
        $files = fn (string $e) => ['schema_version' => 1, 'files' => [], 'error' => $e];
        $golden = fn (string $e) => ['schema_version' => 1, 'cases' => [], 'diffs' => [], 'error' => $e];
        return [
            'list_tests' => $files,
            'signatures' => $files,
            'classify_lines' => $files,
            'complexity' => $files,
            'stub' => fn (string $e) => ['schema_version' => 1, 'stubbed' => [], 'unstubbed' => [], 'error' => $e],
            // With an error, the schema's if/then on ok and findings does not apply.
            'tier0' => fn (string $e) => ['schema_version' => 1, 'ok' => false, 'findings' => [], 'error' => $e],
            'deps_graph' => fn (string $e) => ['schema_version' => 1, 'edges' => [], 'unresolved' => [], 'error' => $e],
            'mutate' => fn (string $e) => [
                'schema_version' => 1, 'mutants' => [], 'not_generated' => [], 'pruned' => [], 'error' => $e,
            ],
            'lemmas' => fn (string $e) => ['schema_version' => 1, 'lemmas' => [], 'error' => $e],
            'golden_record' => $golden,
            'golden_replay' => $golden,
            'capture_traces' => fn (string $e) => [
                'schema_version' => 1, 'runner' => null, 'test_exit' => null, 'traces' => [], 'skipped' => [],
                'error' => $e,
            ],
        ];
    }

    public static function notDone(string $operation): string
    {
        return "the PHP engine does not do {$operation} yet";
    }

    // fuzz: its response schema has no error field, and findings [] with runs 0 would read as "fuzzed, nothing found".
    public const NO_ERROR_FIELD = ['fuzz'];

    /** @param array<string, mixed> $body */
    public static function write(string $io, array $body): void
    {
        $json = json_encode($body, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE
            | JSON_PRESERVE_ZERO_FRACTION | JSON_INVALID_UTF8_SUBSTITUTE | JSON_THROW_ON_ERROR);
        file_put_contents("{$io}/response.json", $json);
    }
}
