<?php
namespace App\Edge;

function loop(int $x): int
{
    while (true) {
    }
}

function variadic(int ...$xs): int
{
    return array_sum($xs) + 1;
}

function byref(int &$x): int
{
    return $x + 1;
}

function gen(int $x): iterable
{
    yield $x + 1;
}

function mk(int $x): \Closure
{
    return fn () => $x + 1;
}

function obj(\DateTime $d): string
{
    return $d->format('y');
}

function quits(int $x): int
{
    if ($x > 40) {
        exit(1);
    }
    return $x;
}

/** @param int[] $xs */
function arr(array $xs): int
{
    return count($xs) > 2 ? array_sum($xs) + 1 : array_sum($xs);
}

function nullable(?int $x): int
{
    return $x ?? -1;
}
