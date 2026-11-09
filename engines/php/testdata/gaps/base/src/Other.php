<?php
namespace App;

const RATE = 3;

function helper(int $x): int
{
    return $x;
}

final class Other
{
    public static function twice(int $x): int
    {
        return $x * 2;
    }

    public function apply(int $x): int
    {
        return $x;
    }
}
