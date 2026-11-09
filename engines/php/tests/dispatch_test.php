<?php
// The operations this engine does not do answer in their own schema with an error; fuzz and unknown operations exit 2
// without a response. runEngine validates every response.json against its schema.

declare(strict_types=1);

function opRequest(string $operation, array $budget = []): array
{
    return ['schema_version' => 1, 'language' => 'php', 'base_ref' => str_repeat('0', 40), 'base_dir' => '/base',
        'candidate_dir' => '/cand', 'changed_files' => [], 'budget' => $budget, 'generators_override' => null,
        'operation' => $operation];
}

$tests = [];
foreach (['list_tests', 'stub', 'signatures', 'tier0', 'classify_lines', 'deps_graph', 'mutate', 'complexity', 'lemmas',
    'golden_record', 'golden_replay', 'capture_traces'] as $op) {
    $tests["{$op} answers its schema with the not-done error"] = function () use ($op): void {
        $r = runEngine(opRequest($op));
        same(0, $r['code'], 'exit code');
        check($r['response'] !== null, 'no response.json');
        same("the PHP engine does not do {$op} yet", $r['response']['error'], 'error');
        same(1, $r['response']['schema_version'], 'schema_version');
    };
}

$tests['fuzz exits 2 and writes no response'] = function (): void {
    $r = runEngine(opRequest('fuzz', ['seconds' => 1, 'seed' => 1]));
    same(2, $r['code'], 'exit code');
    same(null, $r['response'], 'response');
    check(str_contains($r['stderr'], 'the PHP engine does not do fuzz yet'), "stderr: {$r['stderr']}");
};

$tests['an unknown operation exits 2 and writes no response'] = function (): void {
    $r = runEngine(opRequest('teleport'));
    same(2, $r['code'], 'exit code');
    same(null, $r['response'], 'response');
    check(str_contains($r['stderr'], 'unknown operation'), "stderr: {$r['stderr']}");
};

return $tests;
