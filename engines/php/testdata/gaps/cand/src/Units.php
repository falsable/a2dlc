<?php
namespace App;

trait Discount
{
    public function off(int $x): int
    {
        return $x - 2;
    }
}

interface Shape
{
    public function area(): float;
}

enum Status: string
{
    case On = 'on';

    public function label(): string
    {
        return 'ON';
    }
}

const LIMIT = 4;

ini_set('precision', '10');
