<?php
namespace App;

// Per-process state: the count of calls. The candidate no longer exits.
function tick(int $x, int $y): int
{
    static $n = 0;
    $n++;
    return $n;
}
