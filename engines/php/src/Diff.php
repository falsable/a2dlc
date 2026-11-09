<?php
// The diff operation. For each changed .php file: parse both versions (php-parser, names resolved), find the top-level
// functions and public class methods whose code changed (or that use something that changed), and run each version in
// its own worker process on the same Eris-generated inputs. The first difference in return value, thrown exception or
// output (and, for an instance method, the object's public state) is a FAIL with that counterexample. A symbol whose
// text changed but whose code did not is a PASS "equivalent by AST" without running it. What cannot be judged is
// INCONCLUSIVE with its reason; it is never a PASS. That includes code the engine does not run (traits, interfaces,
// enums, constants, top-level statements), a public symbol the candidate removed or hid, a file that does not parse
// and a target whose every call hit a symbol from a file the worker did not load.

namespace A2dlc\PhpEngine;

require_once __DIR__ . '/Codec.php';
require_once __DIR__ . '/Side.php';
require_once __DIR__ . '/ErisShim.php';

use Eris\Generator as ErisGenerator;
use Eris\Generators as G;
use PhpParser\Node;
use PhpParser\Node\Expr;
use PhpParser\Node\Stmt;
use PhpParser\NodeFinder;
use PhpParser\NodeTraverser;
use PhpParser\NodeVisitor;
use PhpParser\NodeVisitor\NameResolver;
use PhpParser\NodeVisitorAbstract;
use PhpParser\Parser;
use PhpParser\ParserFactory;
use PhpParser\PrettyPrinter\Standard;

/** The target cannot be judged; the message is its INCONCLUSIVE reason. */
final class Unjudgeable extends \Exception
{
    public function __construct(string $reason, public readonly bool $typed = true)
    {
        parent::__construct($reason);
    }
}

/** Comments are not code: they are kept aside (for the raw text and @param docs) and removed before printing. */
final class StripComments extends NodeVisitorAbstract
{
    public function enterNode(Node $node)
    {
        $comments = $node->getComments();
        if ($comments) {
            $node->setAttribute('a2dlcComments', implode("\n", array_map(fn ($c) => $c->getText(), $comments)));
            $node->setAttribute('comments', []);
        }
        return null;
    }

    public function leaveNode(Node $node)
    {
        return $node instanceof Stmt\Nop ? NodeVisitor::REMOVE_NODE : null;
    }
}

final class Diff
{
    public const EQUIVALENT_AST = 'equivalent by AST: the code is the same without comments and layout, so the engine did not run it';
    private const ENGINE = 'eris';
    private const SHRINK_S = 30;

    private int $examples;
    private int $seed;
    private float $timeout;
    private string $version;
    private Parser $parser;
    private Standard $printer;

    /** @param array<string, mixed> $req */
    public function __construct(private array $req)
    {
        $budget = is_array($req['budget'] ?? null) ? $req['budget'] : [];
        $this->examples = is_int($budget['examples'] ?? null) && $budget['examples'] >= 1 ? $budget['examples'] : 2000;
        $this->seed = is_int($budget['seed'] ?? null) ? $budget['seed'] : 42;
        $t = $budget['call_timeout_s'] ?? 5;
        $this->timeout = (is_int($t) || is_float($t)) && $t > 0 ? (float) $t : 5.0;
        $this->version = self::erisVersion();
        $this->parser = (new ParserFactory())->createForNewestSupportedVersion();
        $this->printer = new Standard();
    }

    /** The Eris version installed from composer.lock (the image never runs Composer). */
    private static function erisVersion(): string
    {
        $lock = json_decode((string) file_get_contents(dirname(__DIR__) . '/composer.lock'), true);
        foreach ($lock['packages'] ?? [] as $p) {
            if ($p['name'] === 'giorgiosironi/eris') {
                return ltrim($p['version'], 'v');
            }
        }
        return 'unknown';
    }

    /** @return array<string, mixed> */
    public function run(): array
    {
        $targets = [];
        $outOfScope = [];
        foreach ($this->req['changed_files'] as $file) {
            $reason = $this->outOfScope($file);
            if ($reason !== null) {
                $outOfScope[] = ['file' => $file, 'reason' => $reason];
                continue;
            }
            try {
                $found = $this->file($file);
            } catch (\PhpParser\Error $e) {
                // As in the TypeScript engine, a file that does not load is that file's INCONCLUSIVE, not the answer's.
                $targets[] = $this->unjudged($file, $file, 'could not load: ' . $e->getMessage(), null);
                continue;
            }
            if (!$found) {
                $outOfScope[] = ['file' => $file, 'reason' => 'not_exported'];
            }
            array_push($targets, ...$found);
        }
        return ['schema_version' => 1, 'targets' => $targets, 'out_of_scope' => $outOfScope, 'error' => null];
    }

    private function outOfScope(string $file): ?string
    {
        if (!str_ends_with(strtolower($file), '.php')) {
            return 'not_exported';
        }
        if (str_starts_with($file, 'tests/') || str_contains($file, '/tests/') || str_ends_with($file, 'Test.php')) {
            return 'test_code';
        }
        if (!is_file("{$this->req['candidate_dir']}/{$file}")) {
            return 'deleted';
        }
        if (!is_file("{$this->req['base_dir']}/{$file}")) {
            return 'added';
        }
        return null;
    }

    // ---------- symbols ----------

    /** @return array{0: array<string, array<string, mixed>>, 1: string} entities by lowercased id */
    private function entities(string $path, string $file): array
    {
        $code = (string) file_get_contents($path);
        try {
            $stmts = $this->parser->parse($code) ?? [];
        } catch (\PhpParser\Error $e) {
            throw new \PhpParser\Error("{$file} does not parse: " . $e->getRawMessage(), $e->getAttributes());
        }
        $tr = new NodeTraverser();
        $tr->addVisitor(new NameResolver());
        $tr->addVisitor(new StripComments());
        $stmts = $tr->traverse($stmts);

        $out = [];
        $context = []; // everything outside functions and classes: what any symbol of the file may read
        $top = []; // the part of it that is no trait, interface, enum or constant: statements run on load, declare
        $walk = function (array $stmts) use (&$walk, &$out, &$context, &$top, $code): void {
            foreach ($stmts as $s) {
                if ($s instanceof Stmt\Namespace_) {
                    $walk($s->stmts);
                } elseif ($s instanceof Stmt\Use_ || $s instanceof Stmt\GroupUse) {
                    continue; // already applied by NameResolver to every name
                } elseif ($s instanceof Stmt\Function_ || ($s instanceof Stmt\Class_ && $s->name !== null)) {
                    $this->declaration($s, $code, $out);
                } else {
                    $context[] = $s = $this->declarationsIn($s, $code, $out);
                    if ($s instanceof Stmt\Trait_ || $s instanceof Stmt\Interface_ || $s instanceof Stmt\Enum_) {
                        $what = strtolower(rtrim((new \ReflectionClass($s))->getShortName(), '_'));
                        $name = $s->namespacedName->toString();
                        $out['u:' . strtolower($name)] = $this->unit($what, $name, [$s], $s);
                    } elseif ($s instanceof Stmt\Const_) {
                        foreach ($s->consts as $c) {
                            $name = $c->namespacedName->toString();
                            $out['k:' . strtolower($name)] = $this->unit('constant', $name, [new Stmt\Const_([$c])], $c);
                        }
                    } else {
                        $top[] = $s;
                    }
                }
            }
        };
        $walk($stmts);
        // what every symbol refers to (a change here reaches them all); reported through 'top' and the units
        $out['file'] = ['unit' => null] + $this->unit('top-level code', $file, $context, null);
        $out['top'] = $this->unit('top-level code', $file, $top, null);
        return [$out, $code];
    }

    /** @param array<string, array<string, mixed>> $out */
    private function declaration(Stmt\Function_|Stmt\Class_ $s, string $code, array &$out): void
    {
        if ($s instanceof Stmt\Class_) {
            $this->classEntities($s, $code, $out);
            return;
        }
        $name = $s->namespacedName->toString();
        $out['f:' . strtolower($name)] = $this->entity($s, $code, 'function', $name, null, null);
    }

    /**
     * The functions and classes declared inside a top-level block (`if (!function_exists('f')) { function f() ... }`) are
     * symbols like the top-level ones; the block keeps the rest.
     * @param array<string, array<string, mixed>> $out
     */
    private function declarationsIn(Stmt $s, string $code, array &$out): Stmt
    {
        $v = new class extends NodeVisitorAbstract {
            /** @var list<Stmt\Function_|Stmt\Class_> */
            public array $found = [];

            public function enterNode(Node $n)
            {
                if ($n instanceof Stmt\Function_ || ($n instanceof Stmt\Class_ && $n->name !== null)) {
                    $this->found[] = $n;
                    return NodeVisitor::DONT_TRAVERSE_CHILDREN;
                }
                return null;
            }

            public function leaveNode(Node $n)
            {
                return in_array($n, $this->found, true) ? NodeVisitor::REMOVE_NODE : null;
            }
        };
        $s = (new NodeTraverser($v))->traverse([$s])[0];
        foreach ($v->found as $d) {
            $this->declaration($d, $code, $out);
        }
        return $s;
    }

    /**
     * Code the engine does not run but other files can see (a trait, an interface, an enum, a constant, top-level
     * statements): a change to it is reported, never judged.
     * @param list<Node> $nodes
     * @return array<string, mixed>
     */
    private function unit(string $what, string $name, array $nodes, ?Node $at): array
    {
        return ['kind' => 'context', 'unit' => $what, 'name' => $name, 'norm' => $this->printer->prettyPrint($nodes),
            'refs' => [], 'lines' => $at === null ? null : [$at->getStartLine(), $at->getEndLine()]];
    }

    /** @param array<string, array<string, mixed>> $out */
    private function classEntities(Stmt\Class_ $c, string $code, array &$out): void
    {
        $cls = $c->namespacedName->toString();
        $shell = clone $c;
        $shell->stmts = array_values(array_filter($c->stmts, fn ($m) => !$m instanceof Stmt\ClassMethod));
        $out['c:' . strtolower($cls)] = ['kind' => 'context', 'name' => $cls, 'norm' => $this->printer->prettyPrint([$shell]),
            'refs' => $this->refs($shell, $c), 'lines' => [$c->getStartLine(), $c->getEndLine()]];
        foreach ($c->getMethods() as $m) {
            $name = $m->name->toString();
            $kind = strtolower($name) === '__construct' ? 'init' : ($m->isStatic() ? 'static' : 'instance');
            $e = $this->entity($m, $code, $kind, $cls . '.' . $name, $c, $m);
            $e['refs'][] = 'c:' . strtolower($cls);
            if ($kind === 'instance') {
                // every instance method runs on what the constructor built, private state included
                $e['refs'][] = 'm:' . strtolower($cls . '.__construct');
            } elseif ($kind === 'init') {
                // not a target: its effect is judged through the instance methods (D1 targets functions and methods)
                $e['callable'] = false;
            }
            $out['m:' . strtolower($cls . '.' . $name)] = $e;
        }
    }

    /** @return array<string, mixed> */
    private function entity(Stmt\Function_|Stmt\ClassMethod $node, string $code, string $kind, string $name,
        ?Stmt\Class_ $class, ?Stmt\ClassMethod $method): array
    {
        $start = $node->getStartFilePos();
        $raw = ($node->getAttribute('a2dlcComments') ?? '') . substr($code, $start, $node->getEndFilePos() - $start + 1);
        $public = $method === null || $method->isPublic();
        return [
            'kind' => $kind, 'name' => $name, 'node' => $node, 'class' => $class,
            'norm' => $this->printer->prettyPrint([$node]), 'raw' => $raw,
            'lines' => [$node->getStartLine(), $node->getEndLine()],
            'callable' => $public && !($method !== null && $method->isAbstract()),
            'refs' => [...$this->refs($node, $class), 'file'],
        ];
    }

    /** Ids of the functions, methods and classes that a node uses (lowercased: PHP names are case-insensitive). */
    private function refs(Node $node, ?Stmt\Class_ $class): array
    {
        $cls = $class?->namespacedName?->toString();
        $parent = $class?->extends?->toString();
        $resolve = function (Node $n) use ($cls, $parent): ?string {
            if (!$n instanceof Node\Name) {
                return null;
            }
            $s = strtolower($n->toString());
            return match ($s) {
                'self', 'static' => $cls !== null ? strtolower($cls) : null,
                'parent' => $parent !== null ? strtolower($parent) : null,
                default => $s,
            };
        };
        $refs = [];
        foreach ((new NodeFinder())->find([$node], fn ($n) => $n instanceof Expr) as $n) {
            if ($n instanceof Expr\FuncCall && $n->name instanceof Node\Name) {
                $refs[] = 'f:' . strtolower($n->name->toString());
                $ns = $n->name->getAttribute('namespacedName');
                if ($ns instanceof Node\Name) {
                    $refs[] = 'f:' . strtolower($ns->toString());
                }
            } elseif (($n instanceof Expr\MethodCall || $n instanceof Expr\NullsafeMethodCall)
                && $n->var instanceof Expr\Variable && $n->var->name === 'this'
                && $n->name instanceof Node\Identifier && $cls !== null) {
                $refs[] = 'm:' . strtolower($cls . '.' . $n->name->toString());
            } elseif ($n instanceof Expr\StaticCall || $n instanceof Expr\New_ || $n instanceof Expr\ClassConstFetch
                || $n instanceof Expr\StaticPropertyFetch) {
                $c = $resolve($n->class);
                if ($c === null) {
                    continue;
                }
                $refs[] = "c:{$c}";
                if ($n instanceof Expr\StaticCall && $n->name instanceof Node\Identifier) {
                    $refs[] = "m:{$c}." . strtolower($n->name->toString());
                } elseif ($n instanceof Expr\New_) {
                    $refs[] = "m:{$c}.__construct";
                }
            }
        }
        return array_values(array_unique($refs));
    }

    // ---------- one file ----------

    /** @return list<array<string, mixed>> */
    private function file(string $file): array
    {
        [$base] = $this->entities("{$this->req['base_dir']}/{$file}", $file);
        [$cand] = $this->entities("{$this->req['candidate_dir']}/{$file}", $file);
        $changed = [];
        foreach ($cand as $id => $e) {
            if (!isset($base[$id]) || $base[$id]['norm'] !== $e['norm']) {
                $changed[$id] = true;
            }
        }
        foreach ($base as $id => $_) {
            if (!isset($cand[$id])) {
                $changed[$id] = true;
            }
        }
        $own = $changed; // what changed by itself, before the closure
        // Closure, as in the TypeScript engine: whoever uses something that changed may behave differently too.
        do {
            $grew = false;
            foreach ($cand as $id => $e) {
                if (!isset($changed[$id]) && array_filter($e['refs'], fn ($r) => isset($changed[$r]) && $r !== $id)) {
                    $changed[$id] = $grew = true;
                }
            }
        } while ($grew);

        $out = [];
        foreach ($cand as $id => $e) {
            if ($e['kind'] === 'context' || !$e['callable'] || !isset($base[$id]) || !$base[$id]['callable']) {
                continue;
            }
            $t = $this->newTarget($file, $e);
            if (!isset($changed[$id])) {
                if ($base[$id]['raw'] === $e['raw']) {
                    continue;
                }
                $out[] = $t + ['reason' => self::EQUIVALENT_AST];
                continue;
            }
            try {
                $this->judge($t, $file, $base[$id], $e, $base, $cand);
            } catch (Unjudgeable $u) {
                $t['verdict'] = 'INCONCLUSIVE';
                $t['reason'] = mb_substr($u->getMessage(), 0, 300);
                $t['typed'] = $u->typed;
                $t['counterexample'] = null;
            }
            $out[] = $t;
        }

        // What other files can see but the engine does not run: a changed trait, interface, enum, constant or top-level
        // code, and a public function or method that the candidate removed or hid. Base lines are not candidate lines.
        foreach ($base as $id => $b) {
            $c = $cand[$id] ?? null;
            if (isset($b['unit']) && ($c === null || $c['norm'] !== $b['norm'])) {
                $what = $b['unit'] === 'top-level code' ? 'statements outside functions and classes' : $b['name'];
                $out[] = $this->unjudged($file, $b['name'], "unsupported: {$b['unit']}: {$what} "
                    . ($c === null ? 'was removed' : 'changed') . '; ' . self::UNSEEN[$b['unit']], $c['lines'] ?? null);
            } elseif ($b['kind'] !== 'context' && $b['callable'] && !($c['callable'] ?? false)) {
                $how = match (true) {
                    $c === null => 'was removed',
                    $c['node'] instanceof Stmt\ClassMethod && !$c['node']->isPublic() => 'is no longer public',
                    default => 'is now abstract',
                };
                $out[] = $this->unjudged($file, $b['name'], "unsupported: {$b['name']} {$how} in the candidate; "
                    . 'its callers in other files are not run by this engine', $c['lines'] ?? null);
            }
        }

        // A file whose code changed is never "not exported": what changed and reached no target is named here.
        if (!$out) {
            foreach (array_keys($own) as $id) {
                $e = $cand[$id] ?? $base[$id];
                $what = $e['unit'] ?? match (strstr($id, ':', true)) {
                    'c' => 'class body', 'm' => 'method', 'f' => 'function', default => 'top-level code',
                };
                $how = !isset($base[$id]) ? 'was added' : (!isset($cand[$id]) ? 'was removed' : 'changed');
                $out[] = $this->unjudged($file, $e['name'], "unsupported: {$what}: {$e['name']} {$how}, and no public "
                    . 'function or method of this file that the engine runs uses it', $cand[$id]['lines'] ?? null);
            }
        }
        return $out;
    }

    /** Why a change to code the engine does not run is not judged, by unit. */
    private const UNSEEN = [
        'trait' => 'traits are judged through the classes that use them, which this engine does not do',
        'interface' => 'an interface is implemented and called from other files, which this engine does not run',
        'enum' => 'enum cases and methods are not run by this engine',
        'constant' => 'the code in other files that reads it is not run by this engine',
        'top-level code' => 'it runs when the file is loaded, which this engine does not judge',
    ];

    /** @param array<string, mixed> $e */
    private function newTarget(string $file, array $e): array
    {
        $t = [
            'target' => "{$file}::{$e['name']}", 'engine' => self::ENGINE, 'engine_version' => $this->version,
            'verdict' => 'PASS', 'search_budget' => ['examples' => $this->examples, 'seed' => $this->seed],
            'generators' => 'typed_default', 'typed' => true, 'domain_guard' => null, 'equivalence' => 'strict',
            'counterexample' => null,
        ];
        if ($e['lines'] !== null) {
            $t['lines'] = $e['lines'];
        }
        return $t;
    }

    /** An INCONCLUSIVE target for what the engine did not run. @param array{int, int}|null $lines */
    private function unjudged(string $file, string $name, string $reason, ?array $lines): array
    {
        return ['verdict' => 'INCONCLUSIVE', 'reason' => mb_substr($reason, 0, 300)]
            + $this->newTarget($file, ['name' => $name, 'lines' => $lines]);
    }

    // ---------- inputs ----------

    /**
     * Generators for the parameters of a function or method.
     * @return list<ErisGenerator>
     * @throws Unjudgeable
     */
    private function paramGens(Stmt\Function_|Stmt\ClassMethod $fn, array $lits, ?string $ctorOf = null): array
    {
        $doc = (string) ($fn->getAttribute('a2dlcComments') ?? '');
        $gens = [];
        foreach ($fn->params as $p) {
            $name = '$' . (is_string($p->var->name) ? $p->var->name : '?');
            $why = fn (string $what) => $ctorOf === null ? $what : "constructor not derivable: {$ctorOf} {$what}";
            if ($p->variadic) {
                throw new Unjudgeable($ctorOf === null ? "unsupported: variadic: {$name} takes any number of arguments"
                    : $why("takes a variadic {$name}"));
            }
            if ($p->byRef) {
                throw new Unjudgeable($ctorOf === null ? "unsupported: by-reference: {$name} is passed by reference"
                    : $why("takes {$name} by reference"));
            }
            if ($p->type === null) {
                throw new Unjudgeable($ctorOf === null ? "untyped parameter: {$name}" : $why("parameter {$name} has no type"),
                    false);
            }
            $g = $this->gen($p->type, $this->docElement($doc, $name), $lits);
            if ($g === null) {
                $type = self::typeText($p->type);
                throw new Unjudgeable($ctorOf === null ? "type not derivable: {$type} for {$name}"
                    : $why("parameter {$name} has type {$type}"), false);
            }
            $gens[] = $g;
        }
        return $gens;
    }

    private static function typeText(Node $t): string
    {
        return match (true) {
            $t instanceof Node\NullableType => '?' . self::typeText($t->type),
            $t instanceof Node\UnionType => implode('|', array_map(self::typeText(...), $t->types)),
            $t instanceof Node\IntersectionType => implode('&', array_map(self::typeText(...), $t->types)),
            default => $t->toString(),
        };
    }

    /** The scalar element type of an array parameter from its @param (int[], array<int>, list<int>, array<k, int>). */
    private function docElement(string $doc, string $name): ?string
    {
        $scalar = '(int|float|string|bool)';
        $re = '/@param\s+(?:\??' . $scalar . '\[\]|\??(?:array|list)<\s*(?:(?:int|string)\s*,\s*)?' . $scalar . '\s*>)\s+'
            . preg_quote($name, '/') . '\b/';
        return preg_match($re, $doc, $m) ? ($m[1] !== '' ? $m[1] : $m[2]) : null;
    }

    /** @param array{int: list<int>, float: list<float>, string: list<string>} $lits */
    private function gen(Node $t, ?string $element, array $lits): ?ErisGenerator
    {
        if ($t instanceof Node\NullableType) {
            $inner = $this->gen($t->type, $element, $lits);
            return $inner === null ? null : G::oneOf(G::constant(null), $inner);
        }
        if ($t instanceof Node\UnionType) {
            $gens = [];
            foreach ($t->types as $u) {
                $g = $this->gen($u, $element, $lits);
                if ($g === null) {
                    return null;
                }
                $gens[] = $g;
            }
            return G::oneOf(...$gens);
        }
        if (!$t instanceof Node\Identifier) {
            return null; // a class, an interface, an intersection
        }
        $withLits = fn (ErisGenerator $g, array $values) => $values ? G::oneOf($g, G::elements(array_values($values))) : $g;
        return match ($t->toLowerString()) {
            'int' => $withLits(G::int(), $lits['int']),
            'float' => $withLits(G::float(), $lits['float']),
            'string' => $withLits(G::string(), $lits['string']),
            'bool' => G::bool(),
            'null' => G::constant(null),
            'true' => G::constant(true),
            'false' => G::constant(false),
            // shortcut: an array is a list of scalars (int unless its @param says otherwise); maps need a repo override
            'array' => G::seq($this->gen(new Node\Identifier($element ?? 'int'), null, $lits)),
            default => null,
        };
    }

    /** Literals of both versions of a symbol, and their neighbours: thresholds that random inputs rarely hit. */
    private function literals(Node ...$nodes): array
    {
        $lits = ['int' => [], 'float' => [], 'string' => []];
        foreach ((new NodeFinder())->find($nodes, fn ($n) => $n instanceof Node\Scalar) as $n) {
            if ($n instanceof Node\Scalar\Int_) {
                foreach ([$n->value, $n->value - 1, $n->value + 1, -$n->value] as $v) {
                    $lits['int'][$v] = $v;
                }
                $lits['float'][(string) $n->value] = (float) $n->value;
            } elseif ($n instanceof Node\Scalar\Float_) {
                $lits['float'][(string) $n->value] = $n->value;
            } elseif ($n instanceof Node\Scalar\String_ && strlen($n->value) <= 64) {
                $lits['string'][$n->value] = $n->value;
            }
        }
        return array_map(fn ($a) => array_slice(array_values($a), 0, 64), $lits);
    }

    // ---------- judging ----------

    /**
     * @param array<string, mixed> $t the target, PASS until a difference is found
     * @throws Unjudgeable
     */
    private function judge(array &$t, string $file, array $old, array $new, array $baseAll, array $candAll): void
    {
        $node = $new['node'];
        if ($old['kind'] !== $new['kind']) {
            throw new Unjudgeable("constructor not derivable: {$new['name']} is a {$old['kind']} method in the base "
                . "and a {$new['kind']} method in the candidate");
        }
        if ((new NodeFinder())->findFirst($node->stmts ?? [], fn ($n) => $n instanceof Expr\Yield_
            || $n instanceof Expr\YieldFrom) !== null) {
            throw new Unjudgeable("unsupported: generator: {$new['name']} yields, its values are produced lazily");
        }
        $lits = $this->literals($old['node'], $node);
        $kind = $new['kind'];
        $ctorGens = [];
        if ($kind === 'instance') {
            $ctorGens = $this->ctorGens($new['class'], $old['class'], $lits);
        }
        $gens = $this->paramGens($node, $lits);
        $class = $new['class']?->namespacedName?->toString();
        $req = match ($kind) {
            'function' => ['kind' => 'function', 'name' => $new['name']],
            default => ['kind' => $kind, 'class' => $class, 'method' => $node->name->toString()],
        };
        $nCtor = count($ctorGens);

        $base = new Side($this->req['base_dir'], $file, $this->timeout);
        $cand = new Side($this->req['candidate_dir'], $file, $this->timeout);
        $base->start();
        $cand->start();
        $loadError = $cand->loadError ?? $base->loadError;
        if ($loadError !== null) {
            throw new Unjudgeable('could not load: ' . $loadError);
        }

        $stats = ['reached' => false, 'closure' => false, 'missing' => null];
        $last = null;
        $check = function (...$values) use ($base, $cand, $req, $nCtor, &$stats, &$last): void {
            // A side that restarted (exit, fatal, crash) lost its static and global state: the other restarts with it.
            if (!$base->running() || !$cand->running()) {
                $base->start();
                $cand->start();
            }
            $call = $req;
            $call['ctor'] = array_map([Codec::class, 'encode'], array_slice($values, 0, $nCtor));
            $call['args'] = array_map([Codec::class, 'encode'], array_slice($values, $nCtor));
            $o = $base->call($call);
            $n = $cand->call($call);
            // A call ran the code when its constructor did not throw and it found every symbol it used.
            $ran = fn (array $m): bool => empty($m['ctor_threw']) && !isset($m['missing']);
            if ($ran($o) || $ran($n)) {
                $stats['reached'] = true;
            }
            if (isset($o['missing'], $n['missing'])) {
                $stats['missing'] ??= $o['missing'];
                return; // neither side ran: two "not found" messages say nothing about the change
            }
            $stats['closure'] = $stats['closure'] || !empty($o['closure']) || !empty($n['closure']);
            $oText = Codec::json(self::outcome($o));
            $nText = Codec::json(self::outcome($n));
            if ($oText !== $nText) {
                $last = ['args' => Codec::json(Codec::canonical(array_values($values))), 'old' => $oText, 'new' => $nText];
                throw new Mismatch($last);
            }
        };

        try {
            if ($nCtor + count($gens) === 0) {
                $check(); // nothing to generate: one call says it all
            } else {
                $eris = new SeededEris($this->seed);
                $eris->limitTo($this->examples)->minimumEvaluationRatio(0)->shrinkingTimeLimit(self::SHRINK_S)
                    ->forAll(...$ctorGens, ...$gens)
                    ->then($check);
            }
        } catch (CallTimeout) {
            if ($last === null) {
                throw new Unjudgeable(sprintf('timeout: %s s per call', rtrim(rtrim(sprintf('%.3f', $this->timeout), '0'), '.')));
            }
            // A difference was already found; shrinking it further timed out. The last difference stands.
        } catch (\Throwable $e) {
            $mismatch = $e;
            while ($mismatch !== null && !$mismatch instanceof Mismatch) {
                $mismatch = $mismatch->getPrevious();
            }
            if ($mismatch === null) {
                throw new Unjudgeable('could not load: the engine could not run this target: '
                    . get_class($e) . ': ' . $e->getMessage());
            }
            $last = $mismatch->counterexample;
        } finally {
            $base->stop();
            $cand->stop();
        }
        if ($last !== null) {
            $t['verdict'] = 'FAIL';
            $t['counterexample'] = $last;
            return;
        }
        if (!$stats['reached'] && $stats['missing'] !== null) {
            throw new Unjudgeable("could not load: {$stats['missing']}, on every call of both versions (the engine loads "
                . "only {$file} and the tree's vendor/autoload.php)");
        }
        if ($stats['closure']) {
            throw new Unjudgeable("unsupported: closure: {$new['name']} returns a closure, which cannot be compared");
        }
        if (!$stats['reached'] && $kind === 'instance') {
            throw new Unjudgeable("constructor not derivable: {$class} threw on every generated argument list, "
                . 'the method never ran');
        }
    }

    /** What is compared of one call: the value or the exception, what it printed, and the object's public state. */
    private static function outcome(array $m): array
    {
        $out = array_key_exists('throws', $m) ? ['throws' => $m['throws']] : ['ok' => $m['ok'] ?? null];
        if (array_key_exists('output', $m)) {
            $out['output'] = $m['output'];
        }
        if (array_key_exists('state', $m)) {
            $out['state'] = $m['state'];
        }
        return $out;
    }

    /**
     * Generators of the constructor arguments: each side builds its own instance from the same ones.
     * @return list<ErisGenerator>
     * @throws Unjudgeable
     */
    private function ctorGens(Stmt\Class_ $new, Stmt\Class_ $old, array $lits): array
    {
        $cls = $new->namespacedName->toString();
        if ($new->isAbstract()) {
            throw new Unjudgeable("constructor not derivable: {$cls} is abstract");
        }
        $ctor = $new->getMethod('__construct');
        $oldCtor = $old->getMethod('__construct');
        if ($ctor === null) {
            if ($new->extends !== null) {
                throw new Unjudgeable("constructor not derivable: {$cls} inherits its constructor from "
                    . $new->extends->toString() . ', which this file does not show');
            }
            return [];
        }
        if (!$ctor->isPublic()) {
            throw new Unjudgeable("constructor not derivable: {$cls} has a non-public constructor");
        }
        $params = fn (?Stmt\ClassMethod $m) => $m === null ? '' : implode(', ', array_map(
            fn ($p) => $this->printer->prettyPrint([new Stmt\Expression(new Expr\Closure(['params' => [$p]]))]),
            $m->params));
        if ($params($ctor) !== $params($oldCtor)) {
            throw new Unjudgeable("constructor not derivable: {$cls} changed its constructor parameters, both sides "
                . 'cannot be built from the same arguments');
        }
        return $this->paramGens($ctor, $lits, $cls);
    }
}
