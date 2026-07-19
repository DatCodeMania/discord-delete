#!/bin/sh
# Start discord-delete from the folder this script lives in.
# Keep it next to the `discord-delete` program.
#
# On Linux, double-clicking behavior varies by desktop. If your file manager
# offers "Run in Terminal", use that. Otherwise run this from a terminal:
#   ./linux.sh
cd "$(dirname "$0")" || exit 1
exec ./discord-delete "$@"
