<?php
// One side (base or candidate) of a target: a `php worker.php` process with that tree loaded, so the two versions never
// share a process, a class table or global state. A call that does not answer within the per-call timeout kills the
// process; a process that died (exit(), a fatal error) is started again before the next call (Diff restarts both sides
// together, so they keep the same call history).

namespace A2dlc\PhpEngine;

final class CallTimeout extends \Exception
{
}

final class Side
{
    /** @var resource|null */
    private $proc = null;
    /** @var array<int, resource> */
    private array $pipes = [];
    private string $buffer = '';
    public ?string $loadError = null;

    public function __construct(private string $root, private string $file, private float $timeout)
    {
    }

    public function start(): void
    {
        $this->stop();
        $this->loadError = null;
        // An array command runs php itself (no sh -c), so a kill reaches the process that runs the user's code.
        $cmd = [PHP_BINARY, '-d', 'memory_limit=512M', '-d', 'display_errors=0', dirname(__DIR__) . '/worker.php',
            $this->root, $this->file];
        $spec = [0 => ['pipe', 'r'], 1 => ['file', '/dev/null', 'w'], 2 => ['file', '/dev/null', 'w'], 3 => ['pipe', 'w']];
        $this->proc = proc_open($cmd, $spec, $this->pipes, $this->root);
        if (!is_resource($this->proc)) {
            throw new \RuntimeException('could not start php');
        }
        stream_set_blocking($this->pipes[3], false);
        $this->buffer = '';
        $first = $this->readLine(max($this->timeout, 30.0));
        if ($first === null) {
            $this->loadError = 'the file did not finish loading';
        } elseif (isset($first['load_error'])) {
            $this->loadError = (string) $first['load_error'];
        } elseif (!isset($first['ready'])) {
            $this->loadError = 'the worker did not start';
        }
    }

    public function running(): bool
    {
        return $this->proc !== null && proc_get_status($this->proc)['running'];
    }

    /**
     * @param array<string, mixed> $req
     * @return array<string, mixed>
     * @throws CallTimeout
     */
    public function call(array $req): array
    {
        if (!$this->running()) {
            $this->start();
            if ($this->loadError !== null) {
                return ['throws' => 'the worker could not load the file again: ' . $this->loadError];
            }
        }
        $line = Codec::json($req) . "\n";
        if (@fwrite($this->pipes[0], $line) === false) {
            $this->stop();
            return ['throws' => 'crash: the worker stopped'];
        }
        fflush($this->pipes[0]);
        $msg = $this->readLine($this->timeout);
        if ($msg === false) {
            $this->stop();
            return ['throws' => 'crash: the worker stopped without an answer'];
        }
        if ($msg === null) {
            $this->stop();
            throw new CallTimeout();
        }
        // The worker answered from its shutdown function and is exiting: do not race it with the next call.
        if (isset($msg['throws']) && ($msg['throws'] === 'exit' || str_starts_with($msg['throws'], 'fatal: '))) {
            $this->stop();
        }
        return $msg;
    }

    /** @return array<string, mixed>|null|false null on timeout, false at end of stream */
    private function readLine(float $timeout): array|null|false
    {
        $deadline = microtime(true) + $timeout;
        while (($pos = strpos($this->buffer, "\n")) === false) {
            $left = $deadline - microtime(true);
            if ($left <= 0) {
                return null;
            }
            $r = [$this->pipes[3]];
            $w = $e = null;
            $n = @stream_select($r, $w, $e, (int) $left, (int) (($left - (int) $left) * 1e6));
            if ($n === false) {
                return false;
            }
            if ($n === 0) {
                continue;
            }
            $chunk = fread($this->pipes[3], 65536);
            if ($chunk === '' || $chunk === false) {
                if (feof($this->pipes[3])) {
                    return false;
                }
                continue;
            }
            $this->buffer .= $chunk;
        }
        $line = substr($this->buffer, 0, $pos);
        $this->buffer = substr($this->buffer, $pos + 1);
        return json_decode($line, true, 512, JSON_THROW_ON_ERROR);
    }

    public function stop(): void
    {
        if ($this->proc !== null) {
            foreach ($this->pipes as $p) {
                @fclose($p);
            }
            @proc_terminate($this->proc, 9);
            @proc_close($this->proc);
        }
        $this->proc = null;
        $this->pipes = [];
    }

    public function __destruct()
    {
        $this->stop();
    }
}
