<?php
namespace App;

// Each function uses a symbol that lives in another file of the repo, which the engine does not load.
function viaClass(int $x): int
{
    return Other::twice($x);
}

function viaFn(int $x): int
{
    return helper($x);
}

function viaConst(int $x): int
{
    return $x * \App\RATE;
}

final class Calc
{
    public static function run(int $x): int
    {
        return (new Other())->apply($x);
    }
}
