#!/bin/bash

# Mimic a poorly coded shell script that forks a lot of commands.

while true; do
    find $GOPATH -iname "*.go" -print0 | xargs -0 -n 1 awk '{print $1}' | grep ".*" | wc -c
done
