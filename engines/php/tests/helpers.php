<?php
// Shared by the tests: run the engine as the core does (a request in an io folder, the dispatcher as a process) and check
// that every response it writes is valid against its schema in A2DLC_CONTRACTS.

declare(strict_types=1);

require_once __DIR__ . '/Schema.php';

use A2dlc\PhpEngine\Tests\Schema;

const ENGINE = __DIR__ . '/..';
const TESTDATA = __DIR__ . '/../testdata';

// The response schema of each operation.
const SCHEMA_OF = [
    'diff' => 'engine-response', 'list_tests' => 'engine-list-tests-response', 'stub' => 'engine-stub-response',
    'signatures' => 'engine-signatures-response', 'tier0' => 'engine-tier0-response',
    'classify_lines' => 'engine-classify-lines-response', 'deps_graph' => 'engine-deps-response',
    'mutate' => 'engine-mutate-response', 'complexity' => 'engine-complexity-response',
    'lemmas' => 'engine-lemmas-response', 'golden_record' => 'engine-golden-response',
    'golden_replay' => 'engine-golden-response', 'capture_traces' => 'engine-traces-response',
];

function fail(string $msg): never
{
    throw new RuntimeException($msg);
}

function check(bool $cond, string $msg): void
{
    if (!$cond) {
        fail($msg);
    }
}

function same(mixed $want, mixed $got, string $what): void
{
    if ($want !== $got) {
        fail("{$what}: want " . json_encode($want, JSON_UNESCAPED_SLASHES) . ', got ' . json_encode($got, JSON_UNESCAPED_SLASHES));
    }
}

function schema(): Schema
{
    static $s = null;
    $dir = getenv('A2DLC_CONTRACTS');
    if (!$dir || !is_dir($dir)) {
        fail('A2DLC_CONTRACTS must name the contracts/ folder (tests/run.sh mounts it)');
    }
    return $s ??= new Schema($dir);
}

/** Validates a JSON text against a schema file of contracts/ and fails with every error. */
function assertValid(string $json, string $schemaFile): void
{
    $errors = schema()->errors(json_decode($json, false, 512, JSON_THROW_ON_ERROR), $schemaFile);
    check($errors === [], "{$schemaFile}: " . implode('; ', $errors));
}

/**
 * Runs the dispatcher on a request. Returns exit code, stderr and the decoded response (null when none was written);
 * a written response is validated against its operation's schema before anything else looks at it.
 * @return array{code: int, stderr: string, response: ?array}
 */
function runEngine(array $req): array
{
    $io = sys_get_temp_dir() . '/a2dlc-php-io-' . bin2hex(random_bytes(6));
    mkdir($io);
    file_put_contents("{$io}/request.json", json_encode($req, JSON_UNESCAPED_SLASHES));
    $env = getenv() + ['A2DLC_IO' => $io];
    $env['A2DLC_IO'] = $io;
    $p = proc_open([PHP_BINARY, ENGINE . '/dispatch.php'], [1 => ['pipe', 'w'], 2 => ['pipe', 'w']], $pipes, null, $env);
    stream_get_contents($pipes[1]);
    $stderr = stream_get_contents($pipes[2]);
    $code = proc_close($p);
    $response = null;
    if (is_file("{$io}/response.json")) {
        $raw = (string) file_get_contents("{$io}/response.json");
        $op = $req['operation'] ?? 'diff';
        assertValid($raw, (SCHEMA_OF[$op] ?? fail("no schema known for {$op}")) . '.schema.json');
        $response = json_decode($raw, true, 512, JSON_THROW_ON_ERROR);
        unlink("{$io}/response.json");
    }
    unlink("{$io}/request.json");
    rmdir($io);
    return ['code' => $code, 'stderr' => (string) $stderr, 'response' => $response];
}

/** A diff request over testdata/<fixture>/{base,cand}. */
function diffRequest(string $fixture, array $files, ?array $budget = null): array
{
    return [
        'schema_version' => 1, 'language' => 'php', 'base_ref' => str_repeat('0', 40),
        'base_dir' => TESTDATA . "/{$fixture}/base", 'candidate_dir' => TESTDATA . "/{$fixture}/cand",
        'changed_files' => $files, 'budget' => $budget ?? ['examples' => 300, 'seed' => 42],
        'generators_override' => null,
    ];
}

/** Runs a diff and returns its response, failing if the engine did not write one or reported an error. */
function diff(string $fixture, array $files, ?array $budget = null): array
{
    $r = runEngine(diffRequest($fixture, $files, $budget));
    check($r['code'] === 0, "the engine exited {$r['code']}: {$r['stderr']}");
    check($r['response'] !== null, 'the engine wrote no response.json');
    same(null, $r['response']['error'], 'error');
    return $r['response'];
}

/** The target of a response by its id. */
function target(array $response, string $id): array
{
    foreach ($response['targets'] as $t) {
        if ($t['target'] === $id) {
            return $t;
        }
    }
    fail("no target {$id}; targets: " . implode(', ', array_column($response['targets'], 'target')));
}
