<?php
namespace App;

function keep(int $x): int
{
    return $x;
}

class Box
{
    private function open(): int
    {
        return 1;
    }

    public static function make(): int
    {
        return 2;
    }
}
