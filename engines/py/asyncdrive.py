"""Synchronous driver for coroutine functions, so CrossHair diffbehavior can compare them (engine.py, compare).

CrossHair calls plain functions. `sync_of(fn, side)` returns a function with the same signature that runs the coroutine
to completion in a fresh event loop (asyncio.run), so what the code reads from the loop (create_task, gather, locks,
sleep) works. Driving the coroutine by hand with send(None) was measured and rejected: `await asyncio.sleep(0.01)`
yields a future, both versions stop at the same place and CrossHair reports "No differences" without having run
anything past the first await.

A coroutine can wait on something that never happens inside this process (a socket, a future nobody sets, a long
sleep). Each call is bounded by STEP_TIMEOUT_S; past it the call raises Suspended and is counted. At exit the process
prints how many calls of each side returned, raised or were suspended, so that the engine can refuse a PASS that was
reached without comparing a single completed call.

This file is copied into /tmp/a2dlc as a2dlc_asyncdrive.py and imported by the wrapper modules the engine writes.
"""
import asyncio
import atexit
import functools
import inspect
import json
import sys

STEP_TIMEOUT_S = 1.0
MAX_ITEMS = 1000  # an async generator that yields more than this is treated as unbounded
STATS_PREFIX = "A2DLC_ASYNC_STATS "
NOTHING_RAN = "CrossHair ran no call of the "

stats: dict[str, dict] = {side: {"returned": 0, "suspended": 0, "raised": {}} for side in ("old", "new")}


class Suspended(Exception):
    """The call did not finish by itself within STEP_TIMEOUT_S, or an async generator did not stop."""


def _report() -> None:
    sys.stderr.write(STATS_PREFIX + json.dumps(stats) + "\n")


atexit.register(_report)


async def _bounded(awaitable):
    task = asyncio.ensure_future(awaitable)
    done, _ = await asyncio.wait({task}, timeout=STEP_TIMEOUT_S)
    if not done:
        task.cancel()
        raise Suspended(f"did not finish within {STEP_TIMEOUT_S:g} s")
    return task.result()


async def _collect(agen) -> list:
    items = []
    async for item in agen:
        items.append(item)
        if len(items) > MAX_ITEMS:
            raise Suspended(f"async generator yielded more than {MAX_ITEMS} items")
    return items


async def _main(fn, args, kwargs):
    result = fn(*args, **kwargs)
    if inspect.isasyncgen(result):  # an async generator is not awaitable: its result is the list of what it yields
        return await _bounded(_collect(result))
    if inspect.isawaitable(result):
        return await _bounded(result)
    return result  # a decorator turned it into a plain function


def sync_of(fn, side: str):
    """fn as a plain function (same signature and annotations). side: "old" or "new", for the counters."""
    counters = stats[side]

    @functools.wraps(fn)
    def call(*args, **kwargs):
        try:
            value = asyncio.run(_main(fn, args, kwargs))
        except Suspended:
            counters["suspended"] += 1
            raise
        except Exception as e:  # noqa: BLE001 - whatever the code raises is a result to compare
            name = type(e).__name__
            counters["raised"][name] = counters["raised"].get(name, 0) + 1
            raise
        counters["returned"] += 1
        return value

    return call


def doubt_of(output: str) -> str | None:
    """Why "no differences found" over `sync_of` functions cannot be a PASS, from the process output; None if it can."""
    line = next((x for x in reversed(output.splitlines()) if x.startswith(STATS_PREFIX)), None)
    try:
        seen = json.loads(line[len(STATS_PREFIX):]) if line else None
    except ValueError:
        seen = None
    if not isinstance(seen, dict):
        return "the coroutine driver reported no counts, so what was compared is unknown"
    for side in ("old", "new"):
        s = seen.get(side) or {}
        if s.get("suspended"):
            return (f"{s['suspended']} call(s) of the {side} coroutine did not finish within {STEP_TIMEOUT_S:g} s "
                    "(real I/O, a long sleep, a future nobody sets): those paths were not judged")
        if not s.get("returned"):
            if not s.get("raised"):
                return f"{NOTHING_RAN}{side} coroutine: it could not build the arguments, or discarded every path"
            raised = ", ".join(sorted(s["raised"]))
            return f"no call of the {side} coroutine returned ({raised}): only the exceptions were compared"
    return None
