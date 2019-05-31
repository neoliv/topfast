#!/bin/bash

# Mimic a poorly coded shell script that forks a lot of short lived commands.

while true; do
    # Fork a awk+sed+tr+grep+wc for each .go file in $GOPATH
    find $GOPATH -iname "*.go" -print0 |  while read -r -d $'\0' file ; do echo "$file" | awk '{print}' | sed 's/a/A/g'| tr '[A-Z]' '[a-z]' | egrep ".*" | wc -c; done
done
