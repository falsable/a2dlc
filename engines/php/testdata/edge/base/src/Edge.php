<?php
namespace App\Edge;

function loop(int $x): int
{
    return $x;
}

function variadic(int ...$xs): int
{
    return array_sum($xs);
}

function byref(int &$x): int
{
    return $x;
}

function gen(int $x): iterable
{
    yield $x;
}

function mk(int $x): \Closure
{
    return fn () => $x;
}

function obj(\DateTime $d): string
{
    return $d->format('Y');
}

function quits(int $x): int
{
    if ($x > 30) {
        exit(1);
    }
    return $x;
}

/** @param int[] $xs */
function arr(array $xs): int
{
    return array_sum($xs);
}

function nullable(?int $x): int
{
    return $x ?? 0;
}
