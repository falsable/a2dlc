<?php
// The diff operation over the fixtures of testdata/. Every response is validated against engine-response.schema.json by
// runEngine before these assertions run.

declare(strict_types=1);

// The fn fixture runs once with the default budget (the request carries none), as the core sends it by default.
function fnResponse(): array
{
    static $r = null;
    return $r ??= diff('fn', ['src/Price.php'], []);
}

function inconclusive(array $t, string $prefix): void
{
    same('INCONCLUSIVE', $t['verdict'], "{$t['target']} verdict");
    check(str_starts_with($t['reason'] ?? '', $prefix), "{$t['target']} reason: want '{$prefix}...', got '"
        . ($t['reason'] ?? '') . "'");
}

$fn = fn (string $name): array => target(fnResponse(), "src/Price.php::App\\Pricing\\{$name}");

return [
    'every target carries the eris fields and the default budget' => function (): void {
        $lock = json_decode((string) file_get_contents(ENGINE . '/composer.lock'), true);
        $eris = array_values(array_filter($lock['packages'], fn ($p) => $p['name'] === 'giorgiosironi/eris'))[0];
        foreach (fnResponse()['targets'] as $t) {
            same('eris', $t['engine'], 'engine');
            same(ltrim($eris['version'], 'v'), $t['engine_version'], 'engine_version');
            same(['examples' => 2000, 'seed' => 42], $t['search_budget'], 'search_budget');
            same('typed_default', $t['generators'], 'generators');
            same('strict', $t['equivalence'], 'equivalence');
            same(null, $t['domain_guard'], 'domain_guard');
        }
    },
    'a changed function is a FAIL with a counterexample' => function () use ($fn): void {
        $t = $fn('total');
        same('FAIL', $t['verdict'], 'verdict');
        $args = json_decode($t['counterexample']['args'], true);
        check($args[0] > 150, "the counterexample does not reach the change: {$t['counterexample']['args']}");
        same(['ok' => $args[0] + $args[1]], json_decode($t['counterexample']['old'], true), 'old');
        same(['ok' => $args[0] + $args[1] + 1], json_decode($t['counterexample']['new'], true), 'new');
        same([4, 7], $t['lines'], 'lines');
    },
    'Eris shrinks the counterexample to the smallest failing input' => function () use ($fn): void {
        same('[151,0]', $fn('total')['counterexample']['args'], 'args');
    },
    'an equivalent rewrite is a PASS' => function () use ($fn): void {
        $t = $fn('double');
        same('PASS', $t['verdict'], 'verdict');
        same(null, $t['counterexample'], 'counterexample');
        check(!isset($t['reason']), 'a judged PASS has no reason');
    },
    'a change of comments and layout only is a PASS equivalent by AST' => function () use ($fn): void {
        $t = $fn('label');
        same('PASS', $t['verdict'], 'verdict');
        same('equivalent by AST: the code is the same without comments and layout, so the engine did not run it',
            $t['reason'], 'reason');
        same([15, 19], $t['lines'], 'lines');
    },
    'a different exception is a FAIL with the class and message of each' => function () use ($fn): void {
        $t = $fn('check');
        same('FAIL', $t['verdict'], 'verdict');
        same(['throws' => 'InvalidArgumentException: negative'], json_decode($t['counterexample']['old'], true), 'old');
        same(['throws' => 'DomainException: negative'], json_decode($t['counterexample']['new'], true), 'new');
    },
    'a different output is a FAIL with what each side printed' => function () use ($fn): void {
        $t = $fn('greet');
        same('FAIL', $t['verdict'], 'verdict');
        $old = json_decode($t['counterexample']['old'], true);
        same(null, $old['ok'], 'old ok');
        check(str_starts_with($old['output'], 'hi '), "old output: {$old['output']}");
        check(str_starts_with(json_decode($t['counterexample']['new'], true)['output'], 'hello '), 'new output');
    },
    'an untyped parameter is INCONCLUSIVE' => function () use ($fn): void {
        $t = $fn('untyped');
        same('untyped parameter: $x', $t['reason'] ?? null, 'reason');
        inconclusive($t, 'untyped parameter: $x');
        same(false, $t['typed'], 'typed');
    },
    'an unchanged function is no target' => function (): void {
        check(!in_array('src/Price.php::App\\Pricing\\unchanged', array_column(fnResponse()['targets'], 'target'), true),
            'unchanged was judged');
        same([], fnResponse()['out_of_scope'], 'out_of_scope');
    },
    'the same seed gives the same counterexamples' => function (): void {
        $a = diff('fn', ['src/Price.php'], ['examples' => 200, 'seed' => 7]);
        $b = diff('fn', ['src/Price.php'], ['examples' => 200, 'seed' => 7]);
        same(array_column($a['targets'], 'counterexample'), array_column($b['targets'], 'counterexample'), 'counterexamples');
        same(['examples' => 200, 'seed' => 7], $a['targets'][0]['search_budget'], 'search_budget');
    },
    'methods: instance FAIL, state FAIL, static FAIL and a FAIL through a changed private helper' => function (): void {
        $r = diff('cls', ['src/Cart.php']);
        $t = target($r, 'src/Cart.php::App\\Cart.total');
        same('FAIL', $t['verdict'], 'total verdict');
        $args = json_decode($t['counterexample']['args'], true);
        same(7, $args[1], 'total: the method argument (after the constructor argument)');
        // the object's public state after the call is part of what an instance method is compared on
        same(['ok' => $args[0] + 7, 'state' => [['items', []]]], json_decode($t['counterexample']['old'], true),
            'total old');

        $t = target($r, 'src/Cart.php::App\\Cart.add');
        same('FAIL', $t['verdict'], 'add verdict');
        $old = json_decode($t['counterexample']['old'], true);
        $new = json_decode($t['counterexample']['new'], true);
        same(null, $old['ok'], 'add old ok');
        check($old['state'] !== $new['state'], 'add: the public state is what differs');

        same('FAIL', target($r, 'src/Cart.php::App\\Cart.fee')['verdict'], 'fee verdict');
        same('[10]', target($r, 'src/Cart.php::App\\Cart.fee')['counterexample']['args'], 'fee args');
        same('FAIL', target($r, 'src/Cart.php::App\\Cart.viaHelper')['verdict'], 'viaHelper verdict');
        $ids = array_column($r['targets'], 'target');
        check(!in_array('src/Cart.php::App\\Cart.same', $ids, true), 'same was judged');
        check(!in_array('src/Cart.php::App\\Cart.helper', $ids, true), 'a private method is a target');
    },
    'a change in the constructor alone is judged through the instance methods' => function (): void {
        $r = diff('cls', ['src/Acc.php']);
        $t = target($r, 'src/Acc.php::App\\Acc.get');
        same('FAIL', $t['verdict'], 'get verdict');
        same(['ok' => 1, 'state' => []], json_decode($t['counterexample']['new'], true), 'new');
        same(['src/Acc.php::App\\Acc.get'], array_column($r['targets'], 'target'), 'targets (the constructor is none)');
    },
    "a file that does not parse is that file's INCONCLUSIVE, and the other files keep their targets" => function (): void {
        $r = diff('parse', ['src/P.php', 'src/Q.php']);
        $t = target($r, 'src/P.php::src/P.php');
        inconclusive($t, 'could not load: src/P.php does not parse: ');
        check(!isset($t['lines']), 'a file that does not parse has no lines');
        same('FAIL', target($r, 'src/Q.php::inc')['verdict'], 'the other file is still judged');
        same([], $r['out_of_scope'], 'out_of_scope');
    },
    'a constructor that takes an object is INCONCLUSIVE constructor not derivable' => function (): void {
        $t = target(diff('cls', ['src/Svc.php']), 'src/Svc.php::App\\Svc.run');
        inconclusive($t, 'constructor not derivable: App\\Svc parameter $repo has type App\\Repo');
    },
    'test code, added, deleted and non-PHP files are out of scope; a changed constant is not' => function (): void {
        $r = diff('scope', ['tests/CartTest.php', 'src/FooTest.php', 'src/New.php', 'src/Old.php', 'README.md',
            'src/Consts.php']);
        same([
            ['file' => 'tests/CartTest.php', 'reason' => 'test_code'],
            ['file' => 'src/FooTest.php', 'reason' => 'test_code'],
            ['file' => 'src/New.php', 'reason' => 'added'],
            ['file' => 'src/Old.php', 'reason' => 'deleted'],
            ['file' => 'README.md', 'reason' => 'not_exported'],
        ], $r['out_of_scope'], 'out_of_scope');
        // functions in other files read it: a changed constant is code that changed behavior
        same(['src/Consts.php::LIMIT'], array_column($r['targets'], 'target'), 'targets');
        inconclusive($r['targets'][0], 'unsupported: constant: LIMIT changed; ');
        same([2, 2], $r['targets'][0]['lines'], 'lines');
    },
    'a symbol that lives in a file the engine did not load is INCONCLUSIVE could not load, never a PASS' => function (): void {
        $r = diff('gaps', ['src/Uses.php']);
        $t = fn (string $n) => target($r, "src/Uses.php::App\\{$n}");
        inconclusive($t('viaClass'), 'could not load: Class "App\\Other" not found');
        inconclusive($t('viaFn'), 'could not load: Call to undefined function App\\helper()');
        inconclusive($t('viaConst'), 'could not load: Undefined constant "App\\RATE"');
        inconclusive($t('Calc.run'), 'could not load: Class "App\\Other" not found');
    },
    'a restart of one side restarts the other, so per-process state stays in step' => function (): void {
        $t = target(diff('gaps', ['src/Counter.php']), 'src/Counter.php::App\\tick');
        same('FAIL', $t['verdict'], 'verdict');
        $args = json_decode($t['counterexample']['args'], true);
        check($args[0] >= 100, "the counterexample does not reach the change: {$t['counterexample']['args']}");
        same(['throws' => 'exit'], json_decode($t['counterexample']['old'], true), 'old');
        // the base exited on the call before (shrinking repeats the difference): the candidate restarted with it
        same(['ok' => 1], json_decode($t['counterexample']['new'], true), 'new');
    },
    'traits, interfaces, enums, constants and top-level code that changed are INCONCLUSIVE unsupported' => function (): void {
        $r = diff('gaps', ['src/Units.php']);
        $t = fn (string $n) => target($r, "src/Units.php::{$n}");
        inconclusive($t('App\\Discount'), 'unsupported: trait: App\\Discount changed; traits are judged through the classes');
        inconclusive($t('App\\Shape'), 'unsupported: interface: App\\Shape changed; ');
        inconclusive($t('App\\Status'), 'unsupported: enum: App\\Status changed; ');
        inconclusive($t('App\\LIMIT'), 'unsupported: constant: App\\LIMIT changed; ');
        inconclusive($t('src/Units.php'), 'unsupported: top-level code: statements outside functions and classes changed');
        same(5, count($r['targets']), 'targets');
        same([], $r['out_of_scope'], 'out_of_scope');
    },
    'a function declared inside if (!function_exists(...)) is judged like a top-level one' => function (): void {
        $r = diff('gaps', ['src/Helpers.php']);
        same(['src/Helpers.php::App\\half'], array_column($r['targets'], 'target'), 'targets');
        same('FAIL', $r['targets'][0]['verdict'], 'verdict');
    },
    'a removed or no longer public symbol is INCONCLUSIVE, never silent' => function (): void {
        $r = diff('gaps', ['src/Api.php']);
        $gone = target($r, 'src/Api.php::App\\gone');
        inconclusive($gone, 'unsupported: App\\gone was removed in the candidate; ');
        check(!isset($gone['lines']), 'a removed symbol has no candidate lines');
        inconclusive(target($r, 'src/Api.php::App\\Box.open'), 'unsupported: App\\Box.open is no longer public in the candidate');
        same(2, count($r['targets']), 'targets');
    },
    'a change no judged target reaches is named; comments and layout alone stay not_exported' => function (): void {
        $r = diff('gaps', ['src/Priv.php', 'src/Doc.php']);
        inconclusive(target($r, 'src/Priv.php::App\\Priv.helper'), 'unsupported: method: App\\Priv.helper changed, ');
        inconclusive(target($r, 'src/Priv.php::App\\second'), 'unsupported: function: App\\second was added, ');
        same(2, count($r['targets']), 'targets');
        same([['file' => 'src/Doc.php', 'reason' => 'not_exported']], $r['out_of_scope'], 'out_of_scope');
    },
    'what cannot be generated or compared is INCONCLUSIVE with its reason' => function (): void {
        $r = diff('edge', ['src/Edge.php'], ['examples' => 200, 'seed' => 42, 'call_timeout_s' => 1]);
        $t = fn (string $n) => target($r, "src/Edge.php::App\\Edge\\{$n}");
        inconclusive($t('loop'), 'timeout: 1 s per call');
        inconclusive($t('variadic'), 'unsupported: variadic');
        inconclusive($t('byref'), 'unsupported: by-reference');
        inconclusive($t('gen'), 'unsupported: generator');
        inconclusive($t('mk'), 'unsupported: closure');
        inconclusive($t('obj'), 'type not derivable: DateTime for $d');
    },
    'exit() in the user code is an outcome, and arrays and nullable types are generated' => function (): void {
        $r = diff('edge', ['src/Edge.php'], ['examples' => 300, 'seed' => 42, 'call_timeout_s' => 1]);
        $q = target($r, 'src/Edge.php::App\\Edge\\quits');
        same('FAIL', $q['verdict'], 'quits verdict');
        same(['throws' => 'exit'], json_decode($q['counterexample']['old'], true), 'quits old');
        same('FAIL', target($r, 'src/Edge.php::App\\Edge\\arr')['verdict'], 'arr verdict');
        $n = target($r, 'src/Edge.php::App\\Edge\\nullable');
        same('FAIL', $n['verdict'], 'nullable verdict');
        same('[null]', $n['counterexample']['args'], 'nullable args');
    },
    "the tree's vendor/autoload.php is loaded" => function (): void {
        $t = target(diff('load', ['src/UseLib.php']), 'src/UseLib.php::App\\useIt');
        same('FAIL', $t['verdict'], 'verdict');
        same(['ok' => 42], json_decode($t['counterexample']['old'], true), 'old: twice(21) from the vendor autoloader');
    },
    'a file that fails to load is INCONCLUSIVE could not load' => function (): void {
        $t = target(diff('load', ['src/Broken.php']), 'src/Broken.php::App\\broken');
        inconclusive($t, 'could not load: Class "Missing\\Thing" not found');
    },
];
