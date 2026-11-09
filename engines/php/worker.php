<?php
// One side of a comparison: `php worker.php <tree> <file>` loads the tree's vendor/autoload.php (when there is one) and
// the changed file, then answers one call per line of stdin with one JSON line on fd 3. stdout is left to the user's
// code (its echo is captured with an output buffer); the protocol never shares a stream with it.
// It loads nothing of the engine but Codec, so the user's code sees no engine class or autoloader.

require_once __DIR__ . '/src/Codec.php';

use A2dlc\PhpEngine\Codec;

$root = rtrim($argv[1], '/');
$file = $argv[2];
$proto = fopen('php://fd/3', 'w');
$inCall = false;

$send = function (array $msg) use ($proto): void {
    fwrite($proto, json_encode($msg, Codec::FLAGS | JSON_INVALID_UTF8_SUBSTITUTE) . "\n");
    fflush($proto);
};

// Messages carry the tree's path and line numbers, which differ between the base and the candidate for the same code.
$clean = fn (string $m): string => preg_replace('/ on line \d+/', '', str_replace($root . '/', '', $m));
$thrown = fn (Throwable $e): string => get_class($e) . ': ' . $clean($e->getMessage());

$drain = function (): string {
    $out = '';
    while (ob_get_level() > 0) {
        $out = ob_get_clean() . $out;
    }
    return $out;
};

// A user exit() or a fatal error ends the process: the call in progress still gets its answer.
register_shutdown_function(function () use (&$inCall, $send, $drain, $clean): void {
    $err = error_get_last();
    $fatal = $err !== null && in_array($err['type'], [E_ERROR, E_PARSE, E_CORE_ERROR, E_COMPILE_ERROR], true);
    if ($inCall === 'load') {
        $drain();
        $send(['load_error' => $fatal ? $clean($err['message']) : 'the file exited while loading']);
    } elseif ($inCall) {
        $out = $drain();
        $msg = ['throws' => $fatal ? 'fatal: ' . $clean($err['message']) : 'exit'];
        if ($out !== '') {
            $msg['output'] = Codec::canonical($out);
        }
        $send($msg);
    }
});

chdir($root);
$inCall = 'load';
ob_start();
try {
    if (is_file("{$root}/vendor/autoload.php")) {
        require_once "{$root}/vendor/autoload.php";
    }
    require_once "{$root}/{$file}";
} catch (Throwable $e) {
    $inCall = false;
    $drain();
    $send(['load_error' => $clean($e->getMessage())]);
    exit(0);
}
$inCall = false;
$drain();
$send(['ready' => true]);

while (($line = fgets(STDIN)) !== false) {
    $req = json_decode($line, true);
    $args = array_map([Codec::class, 'decode'], $req['args']);
    $ctor = array_map([Codec::class, 'decode'], $req['ctor'] ?? []);
    $msg = [];
    $obj = null;
    $inCall = true;
    ob_start();
    try {
        switch ($req['kind']) {
            case 'function':
                $value = ($req['name'])(...$args);
                break;
            case 'static':
                $value = [$req['class'], $req['method']](...$args);
                break;
            default: // an instance method: each side builds its own object from the same constructor arguments
                try {
                    $obj = new ($req['class'])(...$ctor);
                } catch (Throwable $e) {
                    $msg['ctor_threw'] = true;
                    throw $e;
                }
                $value = $obj->{$req['method']}(...$args);
        }
        $msg['ok'] = Codec::canonical($value);
        if ($value instanceof Closure) {
            $msg['closure'] = true;
        }
    } catch (Throwable $e) {
        $msg['throws'] = $thrown($e);
    }
    $inCall = false;
    $out = $drain();
    if ($out !== '') {
        $msg['output'] = Codec::canonical($out);
    }
    if ($req['kind'] === 'instance' && $obj !== null) {
        $msg['state'] = Codec::canonical($obj)['props'];
    }
    $send($msg);
}
