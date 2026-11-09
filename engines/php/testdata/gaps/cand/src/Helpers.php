<?php
namespace App;

if (!function_exists('App\half')) {
    function half(int $x): int
    {
        return $x >> 1;
    }
}
