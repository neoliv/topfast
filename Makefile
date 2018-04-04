# Build the test nlstats binary.
# The nlstats.c is used from nlstat.go.

all: nlstats

nlstats: nlstats.c
	gcc -D NO_GO -Wall -O -g -o nlstats nlstats.c
