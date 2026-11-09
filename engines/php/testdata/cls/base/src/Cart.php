<?php
namespace App;

class Cart
{
    public array $items = [];

    public function __construct(private int $base)
    {
    }

    public function total(int $n): int
    {
        return $this->base + $n;
    }

    public function add(int $x): void
    {
        $this->items[] = $x;
    }

    public static function fee(int $x): int
    {
        return $x > 10 ? 1 : 0;
    }

    public function viaHelper(int $x): int
    {
        return $this->helper($x);
    }

    private function helper(int $x): int
    {
        return $x + 1;
    }

    public function same(int $x): int
    {
        return $x;
    }
}
