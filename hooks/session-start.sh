#!/bin/bash
# SessionStart(compact|resume): inject the checkpoint (with the freshness check) into the fresh session.
exec cox hook session-start
