<?php
namespace App;

function first(): int
{
    return 1;
}

function second(): int
{
    return 2;
}

class Priv
{
    private function helper(int $x): int
    {
        return $x + 1;
    }
}
