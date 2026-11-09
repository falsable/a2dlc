<?php
namespace App;

function useIt(int $x): int
{
    return \Lib\twice($x);
}
