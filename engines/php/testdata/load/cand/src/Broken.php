<?php
namespace App;

class Child extends \Missing\Thing
{
}

function broken(int $x): int
{
    return $x + 1;
}
