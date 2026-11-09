<?php
// Entry point of the PHP engine: routes each operation. Only an absent operation or `diff` is judged; every other
// operation answers in its response schema with an error, and fuzz or an unknown operation exits 2 without a response.

declare(strict_types=1);

require __DIR__ . '/vendor/autoload.php';
require_once __DIR__ . '/src/Responses.php';

use A2dlc\PhpEngine\Responses;

$io = getenv('A2DLC_IO') ?: '/io';
$req = json_decode((string) file_get_contents("{$io}/request.json"), true, 512, JSON_THROW_ON_ERROR);
$operation = $req['operation'] ?? 'diff';
$bodies = Responses::errorBodies();

if ($operation === 'diff') {
    require_once __DIR__ . '/src/Diff.php';
    Responses::write($io, (new A2dlc\PhpEngine\Diff($req))->run());
} elseif (is_string($operation) && array_key_exists($operation, $bodies)) {
    Responses::write($io, $bodies[$operation](Responses::notDone($operation)));
} elseif (in_array($operation, Responses::NO_ERROR_FIELD, true)) {
    fwrite(STDERR, Responses::notDone($operation) . "; its response schema has no error field\n");
    exit(2);
} else {
    fwrite(STDERR, 'unknown operation: ' . json_encode($operation) . "\n");
    exit(2);
}
