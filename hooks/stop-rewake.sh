#!/bin/bash
# Stop (asyncRewake): wait for a watcher wake while idle, then open a new leader turn (exit 2); urgent wakes rewake at
# once, routine ones batch, and on timeout a still-open dispatched story ticks so the next turn re-arms the waiter.
exec cox hook stop-rewake
