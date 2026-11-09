<?php
namespace App;

interface Repo
{
}

class Svc
{
    public function __construct(private Repo $repo)
    {
    }

    public function run(int $x): int
    {
        return $x + 1;
    }
}
