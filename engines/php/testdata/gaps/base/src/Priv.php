<?php
namespace App;

function first(): int
{
    return 1;
}

class Priv
{
    private function helper(int $x): int
    {
        return $x;
    }
}
