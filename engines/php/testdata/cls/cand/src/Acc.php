<?php
namespace App;

class Acc
{
    private int $v;

    public function __construct(int $v)
    {
        $this->v = $v + 1;
    }

    public function get(): int
    {
        return $this->v;
    }
}
