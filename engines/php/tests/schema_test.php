<?php
// The validator itself: the contract's PHP examples pass or fail as the contract says, and the checks the engine's
// responses rely on (required, closed objects, unevaluatedProperties, the engine's budget branch) catch what they should.

declare(strict_types=1);

$contracts = fn (): string => getenv('A2DLC_CONTRACTS') ?: fail('A2DLC_CONTRACTS must name the contracts/ folder');
$errorsOf = fn (string $json): array => schema()->errors(json_decode($json, false), 'engine-response.schema.json');

return [
    'the valid PHP examples of the contract pass' => function () use ($contracts): void {
        foreach (['engine-response__php-fail.json', 'engine-response__php-pass.json'] as $f) {
            assertValid((string) file_get_contents($contracts() . "/examples/valid/{$f}"), 'engine-response.schema.json');
        }
    },
    'the invalid PHP example of the contract fails' => function () use ($contracts, $errorsOf): void {
        $json = (string) file_get_contents($contracts() . '/examples/invalid/engine-response__php-budget-from-another-engine.json');
        check($errorsOf($json) !== [], 'a crosshair budget on an eris target passed');
    },
    'an extra field on a target, a missing field and a wrong enum are caught' => function () use ($contracts, $errorsOf): void {
        $base = json_decode((string) file_get_contents($contracts() . '/examples/valid/engine-response__php-pass.json'), true);
        $extra = $base;
        $extra['targets'][0]['surprise'] = 1;
        check($errorsOf(json_encode($extra)) !== [], 'an extra target field passed');
        $missing = $base;
        unset($missing['targets'][0]['typed']);
        check($errorsOf(json_encode($missing)) !== [], 'a missing target field passed');
        $enum = $base;
        $enum['targets'][0]['engine'] = 'phpunit';
        check($errorsOf(json_encode($enum)) !== [], 'an unknown engine passed');
        $top = $base;
        $top['extra'] = true;
        check($errorsOf(json_encode($top)) !== [], 'an extra top-level field passed');
        $inc = $base;
        $inc['targets'][0]['verdict'] = 'INCONCLUSIVE';
        check($errorsOf(json_encode($inc)) !== [], 'an INCONCLUSIVE target without reason passed');
    },
];
