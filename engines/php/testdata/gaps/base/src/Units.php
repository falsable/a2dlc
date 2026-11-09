<?php
namespace App;

trait Discount
{
    public function off(int $x): int
    {
        return $x - 1;
    }
}

interface Shape
{
    public function area(): int;
}

enum Status: string
{
    case On = 'on';

    public function label(): string
    {
        return 'on';
    }
}

const LIMIT = 3;

ini_set('precision', '14');
