<?php
namespace App;

// Per-process state: the count of calls. The base exits from 100 on, which restarts its worker; $y only gives Eris
// something to shrink after the first difference.
function tick(int $x, int $y): int
{
    static $n = 0;
    $n++;
    if ($x >= 100) {
        exit();
    }
    return $n;
}
