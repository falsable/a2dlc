<?php
namespace App\Pricing;

function total(int $a, int $b): int
{
    return $a + $b;
}

function double(int $x): int
{
    return $x * 2;
}

function label(string $name, float $price): string
{
    return $name . ': ' . number_format($price, 2);
}

function check(int $x): int
{
    if ($x < 0) {
        throw new \InvalidArgumentException('negative');
    }
    return $x;
}

function greet(string $who): void
{
    echo "hi {$who}";
}

function untyped($x)
{
    return $x;
}

function unchanged(int $x): int
{
    return $x;
}
