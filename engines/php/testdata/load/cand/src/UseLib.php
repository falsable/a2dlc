<?php
namespace App;

function useIt(int $x): int
{
    return \Lib\twice($x) + ($x > 20 ? 1 : 0);
}
