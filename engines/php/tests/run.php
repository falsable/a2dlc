<?php
// Test runner: every tests/*_test.php returns [name => closure]. A test fails by throwing; the process exits 1 if any did.

declare(strict_types=1);

require __DIR__ . '/helpers.php';

$passed = 0;
$failed = [];
foreach (glob(__DIR__ . '/*_test.php') as $file) {
    foreach (require $file as $name => $test) {
        $label = basename($file, '.php') . ': ' . $name;
        try {
            $test();
            $passed++;
            echo "ok   {$label}\n";
        } catch (Throwable $e) {
            $failed[] = $label;
            echo "FAIL {$label}\n     " . str_replace("\n", "\n     ", $e->getMessage()) . "\n";
        }
    }
}
printf("\n%d passed, %d failed\n", $passed, count($failed));
exit($failed ? 1 : 0);
