<?php
// A JSON Schema validator for the subset contracts/ uses: type, enum, const, pattern, minLength, minimum, maximum,
// exclusiveMinimum, minItems, maxItems, uniqueItems, minProperties, maxProperties, required, properties,
// additionalProperties, items, $ref (into the same file or a sibling file, with a JSON pointer), allOf, anyOf, oneOf,
// if/then/else and unevaluatedProperties false (properties evaluated by the subschemas that applied).
// Documents are decoded with objects as stdClass, so {} and [] stay apart.

namespace A2dlc\PhpEngine\Tests;

final class Schema
{
    /** @var array<string, mixed> decoded schema files by path */
    private array $files = [];

    public function __construct(private string $dir)
    {
    }

    /** @return list<string> errors of $doc (decoded with objects) against the schema in $file */
    public function errors(mixed $doc, string $file): array
    {
        $errors = [];
        $this->check($doc, $this->load($file), $file, '$', $errors);
        return $errors;
    }

    private function load(string $file): mixed
    {
        return $this->files[$file] ??= json_decode((string) file_get_contents("{$this->dir}/{$file}"), false, 512,
            JSON_THROW_ON_ERROR);
    }

    /** @return array{0: mixed, 1: string} the schema a $ref points to and the file it lives in */
    private function resolve(string $ref, string $file): array
    {
        [$path, $pointer] = array_pad(explode('#', $ref, 2), 2, '');
        $file = $path === '' ? $file : $path;
        $node = $this->load($file);
        foreach (array_filter(explode('/', $pointer), fn ($p) => $p !== '') as $part) {
            $part = str_replace(['~1', '~0'], ['/', '~'], $part);
            $node = is_object($node) ? $node->{$part} : $node[(int) $part];
        }
        return [$node, $file];
    }

    private static function typeOf(mixed $v): string
    {
        return match (true) {
            $v === null => 'null', is_bool($v) => 'boolean', is_int($v) => 'integer', is_float($v) => 'number',
            is_string($v) => 'string', is_array($v) => 'array', default => 'object',
        };
    }

    /**
     * Validates and returns the property names this schema evaluated (for unevaluatedProperties).
     * @param list<string> $errors
     * @return array<string, true>
     */
    private function check(mixed $v, mixed $s, string $file, string $at, array &$errors): array
    {
        if ($s === true) {
            return [];
        }
        if ($s === false) {
            $errors[] = "{$at}: no value is allowed here";
            return [];
        }
        $seen = [];
        if (isset($s->{'$ref'})) {
            [$target, $targetFile] = $this->resolve($s->{'$ref'}, $file);
            $seen += $this->check($v, $target, $targetFile, $at, $errors);
        }
        if (isset($s->type)) {
            $types = (array) $s->type;
            $t = self::typeOf($v);
            if (!in_array($t, $types, true) && !($t === 'integer' && in_array('number', $types, true))) {
                $errors[] = "{$at}: is {$t}, the schema says " . implode(' or ', $types);
                return $seen;
            }
        }
        if (property_exists($s, 'const') && !self::same($v, $s->const)) {
            $errors[] = "{$at}: is not the constant " . json_encode($s->const);
        }
        if (isset($s->enum) && !array_filter($s->enum, fn ($e) => self::same($v, $e))) {
            $errors[] = "{$at}: " . json_encode($v) . ' is outside its enum';
        }
        if (is_string($v)) {
            if (isset($s->minLength) && mb_strlen($v) < $s->minLength) {
                $errors[] = "{$at}: shorter than {$s->minLength}";
            }
            if (isset($s->pattern) && !preg_match('/' . str_replace('/', '\/', $s->pattern) . '/u', $v)) {
                $errors[] = "{$at}: " . json_encode($v) . " does not match {$s->pattern}";
            }
        }
        if (is_int($v) || is_float($v)) {
            if (isset($s->minimum) && $v < $s->minimum) {
                $errors[] = "{$at}: below {$s->minimum}";
            }
            if (isset($s->maximum) && $v > $s->maximum) {
                $errors[] = "{$at}: above {$s->maximum}";
            }
            if (isset($s->exclusiveMinimum) && $v <= $s->exclusiveMinimum) {
                $errors[] = "{$at}: not above {$s->exclusiveMinimum}";
            }
        }
        if (is_array($v)) {
            if (isset($s->minItems) && count($v) < $s->minItems) {
                $errors[] = "{$at}: fewer than {$s->minItems} items";
            }
            if (isset($s->maxItems) && count($v) > $s->maxItems) {
                $errors[] = "{$at}: more than {$s->maxItems} items";
            }
            if (!empty($s->uniqueItems) && count(array_unique(array_map('json_encode', $v))) !== count($v)) {
                $errors[] = "{$at}: items are not unique";
            }
            if (isset($s->items)) {
                foreach ($v as $i => $item) {
                    $this->check($item, $s->items, $file, "{$at}[{$i}]", $errors);
                }
            }
        }
        if (is_object($v)) {
            $props = get_object_vars($v);
            foreach ($s->required ?? [] as $r) {
                if (!array_key_exists($r, $props)) {
                    $errors[] = "{$at}: missing {$r}";
                }
            }
            if (isset($s->minProperties) && count($props) < $s->minProperties) {
                $errors[] = "{$at}: fewer than {$s->minProperties} properties";
            }
            if (isset($s->maxProperties) && count($props) > $s->maxProperties) {
                $errors[] = "{$at}: more than {$s->maxProperties} properties";
            }
            $declared = isset($s->properties) ? get_object_vars($s->properties) : [];
            foreach ($props as $k => $x) {
                if (array_key_exists($k, $declared)) {
                    $this->check($x, $declared[$k], $file, "{$at}.{$k}", $errors);
                    $seen[$k] = true;
                } elseif (isset($s->additionalProperties) && $s->additionalProperties === false) {
                    $errors[] = "{$at}: field outside the schema: {$k}";
                } elseif (isset($s->additionalProperties)) {
                    $this->check($x, $s->additionalProperties, $file, "{$at}.{$k}", $errors);
                    $seen[$k] = true;
                }
            }
        }
        foreach ($s->allOf ?? [] as $sub) {
            $seen += $this->check($v, $sub, $file, $at, $errors);
        }
        foreach (['anyOf', 'oneOf'] as $kw) {
            if (!isset($s->{$kw})) {
                continue;
            }
            $passing = 0;
            foreach ($s->{$kw} as $sub) {
                $e = [];
                $got = $this->check($v, $sub, $file, $at, $e);
                if (!$e) {
                    $passing++;
                    $seen += $got;
                }
            }
            if ($kw === 'anyOf' ? $passing === 0 : $passing !== 1) {
                $errors[] = "{$at}: {$passing} branches of {$kw} match";
            }
        }
        if (isset($s->if)) {
            $e = [];
            $got = $this->check($v, $s->if, $file, $at, $e);
            $branch = $e ? ($s->else ?? null) : ($s->then ?? null);
            if (!$e) {
                $seen += $got;
            }
            if ($branch !== null) {
                $seen += $this->check($v, $branch, $file, $at, $errors);
            }
        }
        if (is_object($v) && isset($s->unevaluatedProperties) && $s->unevaluatedProperties === false) {
            foreach (array_keys(get_object_vars($v)) as $k) {
                if (!isset($seen[$k])) {
                    $errors[] = "{$at}: field outside the schema: {$k}";
                }
            }
        }
        return $seen;
    }

    private static function same(mixed $a, mixed $b): bool
    {
        return json_encode($a) === json_encode($b);
    }
}
