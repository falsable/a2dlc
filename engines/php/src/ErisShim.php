<?php
// Eris outside PHPUnit. Eris\Quantifier\Evaluation only treats PHPUnit\Framework\AssertionFailedError as a failing
// example (and only then shrinks); PHPUnit is not installed in the image, so the driver declares that class itself.
// Only the driver loads this file: the workers, which load the user's autoloader, never see it.

namespace PHPUnit\Framework {
    if (!class_exists(AssertionFailedError::class, false)) {
        class AssertionFailedError extends \Exception
        {
        }
    }
}

namespace A2dlc\PhpEngine {
    /** A difference between the two sides; Eris rethrows the one of the smallest input it shrank to. */
    final class Mismatch extends \PHPUnit\Framework\AssertionFailedError
    {
        public function __construct(public readonly array $counterexample)
        {
            parent::__construct('the two versions differ');
        }
    }

    /** Eris\Facade seeds from ERIS_SEED or the clock (a seed of 0 reads as unset): the request's seed is set directly. */
    final class SeededEris extends \Eris\Facade
    {
        public function __construct(int $seed)
        {
            parent::__construct();
            $this->seed = $seed === PHP_INT_MIN ? 0 : abs($seed);
        }
    }
}
