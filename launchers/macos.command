#!/bin/sh
# Double-click this in Finder to start discord-delete.
# Keep it in the same folder as the `discord-delete` program.
# It opens Terminal, moves into this folder, and runs the tool
cd "$(dirname "$0")" || exit 1
exec ./discord-delete "$@"
