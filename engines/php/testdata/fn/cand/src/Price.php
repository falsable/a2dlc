<?php
namespace App\Pricing;

function total(int $a, int $b): int
{
    return $a + $b + ($a > 150 ? 1 : 0);
}

function double(int $x): int
{
    return $x + $x;
}

/** Formats a price with two decimals. */
function label(string $name,   float $price): string {
    // the layout and this comment are the only changes
    return $name . ': '
        . number_format($price, 2);
}

function check(int $x): int
{
    if ($x < 0) {
        throw new \DomainException('negative');
    }
    return $x;
}

function greet(string $who): void
{
    echo "hello {$who}";
}

function untyped($x)
{
    return $x . '';
}

function unchanged(int $x): int
{
    return $x;
}
