<?php
namespace App;

function keep(int $x): int
{
    return $x;
}

function gone(int $x): int
{
    return $x;
}

class Box
{
    public function open(): int
    {
        return 1;
    }

    public static function make(): int
    {
        return 2;
    }
}
