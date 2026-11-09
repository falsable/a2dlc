<?php
// Values as JSON, shared by the driver and the workers. Arguments travel driver -> worker with encode/decode (lossless for
// scalars, lists and maps); results travel back as canonical() data, which is what the two sides are compared on.
// It has no dependencies: worker.php requires it before loading the user's tree.

namespace A2dlc\PhpEngine;

final class Codec
{
    private const DEPTH = 8;
    public const FLAGS = JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_PRESERVE_ZERO_FRACTION
        | JSON_THROW_ON_ERROR;

    /** A transport form of a generated argument: bytes that are not UTF-8 and non-finite floats get a marker object. */
    public static function encode(mixed $v): mixed
    {
        if (is_float($v) && !is_finite($v)) {
            return ['__float' => is_nan($v) ? 'NAN' : ($v > 0 ? 'INF' : '-INF')];
        }
        if (is_string($v) && !mb_check_encoding($v, 'UTF-8')) {
            return ['__bytes' => base64_encode($v)];
        }
        if (is_array($v)) {
            $out = [];
            foreach ($v as $k => $x) {
                $out[] = [$k, self::encode($x)];
            }
            return ['__array' => $out];
        }
        return $v;
    }

    public static function decode(mixed $v): mixed
    {
        if (!is_array($v)) {
            return $v;
        }
        if (array_key_exists('__float', $v)) {
            return ['NAN' => NAN, 'INF' => INF, '-INF' => -INF][$v['__float']];
        }
        if (array_key_exists('__bytes', $v)) {
            return base64_decode($v['__bytes']);
        }
        $out = [];
        foreach ($v['__array'] as [$k, $x]) {
            $out[$k] = self::decode($x);
        }
        return $out;
    }

    /**
     * Plain data equal for equal observable values: an int and a float stay apart, array key order is kept (it is
     * observable), an object is its class and public properties (the two sides load the same class names).
     */
    public static function canonical(mixed $v, int $depth = 0, array $seen = []): mixed
    {
        if ($v === null || is_bool($v) || is_int($v)) {
            return $v;
        }
        if (is_float($v)) {
            if (!is_finite($v)) {
                return ['__float' => is_nan($v) ? 'NAN' : ($v > 0 ? 'INF' : '-INF')];
            }
            return $v;
        }
        if (is_string($v)) {
            return mb_check_encoding($v, 'UTF-8') ? $v : ['__bytes' => base64_encode($v)];
        }
        if ($depth > self::DEPTH) {
            return ['__deep' => get_debug_type($v)];
        }
        if (is_array($v)) {
            if (array_is_list($v)) {
                return array_map(fn ($x) => self::canonical($x, $depth + 1, $seen), $v);
            }
            $out = [];
            foreach ($v as $k => $x) {
                $out[] = [$k, self::canonical($x, $depth + 1, $seen)];
            }
            return ['__map' => $out];
        }
        if ($v instanceof \Closure) {
            return ['__closure' => true];
        }
        if (is_object($v)) {
            $id = spl_object_id($v);
            if (in_array($id, $seen, true)) {
                return ['__cycle' => get_class($v)];
            }
            $seen[] = $id;
            $props = [];
            foreach (get_object_vars($v) as $k => $x) {
                $props[] = [$k, self::canonical($x, $depth + 1, $seen)];
            }
            return ['__object' => get_class($v), 'props' => $props];
        }
        return ['__other' => get_debug_type($v)];
    }

    public static function json(mixed $v): string
    {
        return json_encode($v, self::FLAGS);
    }
}
