package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path"
	"runtime"
	"syscall"
	"time"
)

var myUsage = func() {
	c := path.Base(os.Args[0])
	fmt.Fprintf(os.Stderr, "Usage for %s:\n", c)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
Display statistics about (short lived) processes.
Usual tools like top are sampling the running processes and thus will miss most of the short lived ones.
This tool does that but also uses the taskstats netlink kernel API to get stats about all dying processes. So it should be able to give a more accurate view of by command CPU usage.

Note that you need to have root privileges.
You can ask for an updated display by sending SIGUSR1 (eg: pkill -USR1 %s)
You can reset the counters with SIGUSR2.

eg: %s -i 30s -o /tmp/%s.out
  This will store stats every 30s in the %s.out file.

eg: %s -c -i 10m | tee /tmp/%s.out
  This will display and store stats every 10m. But the -c reset the counters so stats displayed are for the last 10m only.

Notes about the displayed informations:

The execution time (et) is the user+system CPU usage. 
The exucution count (ec) is the number of times the command was executed.

The header should be self explanatory.

The histogram helps understand the processes execution time distribution. Every time a process dies its execution time is accounted in a power of 10 ns histogram.

The first list displays statistics on a per command basis. The default ordering sort them by execution time. This is the sum of execution time for all instances of the same command. eg: all ´grep´ forked on the server will be shown as one grep line.
eg: ??

The second list displays statistics for a command and all its subprocesses. The displayed counters (et, ec, ...) are sums for the command and all its descendant subprocesses.
eg??
 
You can sort commands by execution time of number of executions.

If you need more help feel free to contact Olivier Arsac topfast@arsac.org.
`, c, c, c, c, c, c)
}

var sortKey string
var outfn string
var out *os.File
var interval time.Duration
var top int
var raw, clear, hist bool
var cpuNb uint // Number of CPUs(cores) on this server. Set during init().

// Check e, if not nil print to stderr and exit.
func check(e error) {
	if e != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", e)
		os.Exit(1)
	}
}

func init() {
	cpuNb = uint(runtime.NumCPU())
}

// cpuPercent return the cpu percent usage given an execution time and a period over which it occured. It takes the number of CPUs into account to return 100% only if all CPUs are used full time.
func cpuPercent(et, st float64) float32 {
	return float32(100 * et / (float64(cpuNb) * st))
}

func parseOpts() {
	sortCriteria = scTime
	flag.Usage = myUsage
	flag.StringVar(&outfn, "o", "", "output file (default is stdout).")
	flag.StringVar(&sortKey, "s", "time", "sort criteria (time or count, default is time).")
	defi, _ := time.ParseDuration("10s")
	flag.DurationVar(&interval, "i", defi, "interval between automatic stats output (eg: 30s, 10m, 2h).")
	flag.BoolVar(&raw, "r", false, "output stats in a raw format easier to parse unsing scripts).")
	flag.BoolVar(&clear, "c", false, "clear counters every time we display stats.")
	flag.BoolVar(&hist, "H", false, "display execution time history.")
	flag.IntVar(&top, "t", 10, "number of lines in the top sections.")
	flag.Parse()
	switch sortKey {
	case "count":
		sortCriteria = scCount
	case "time":
		sortCriteria = scTime
	default:
		check(fmt.Errorf("Unknown sort criteria '%s'. Use -s 'count' or 'time'.", sortKey))
	}
	if outfn != "" {
		var err error
		out, err = os.Create(outfn)
		check(err)
	} else {
		out = os.Stdout
	}
}

// Handle signals (output stats).
func trap() {
	c := make(chan os.Signal, 1)
	//signal.Notify(c, os.Interrupt)
	signal.Notify(c, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGTERM, os.Interrupt)
	for s := range c {
		stats()
		switch s {
		case syscall.SIGTERM, os.Interrupt:
			fmt.Fprintf(out, "Received %s Signal. Exiting.\n", s)
			os.Exit(0)
		case syscall.SIGUSR2:
			clearCounters()
		}

	}
}

// Output stats periodicaly.
func tickDisplay(i time.Duration) {
	ticker := time.NewTicker(i)
	for _ = range ticker.C {
		stats()
		if clear {
			clearCounters()
		}
	}
}

// Clean process info map periodicaly.
func tickCPIs(i time.Duration) {
	ticker := time.NewTicker(i)
	for _ = range ticker.C {
		cleanProcInfos()
	}
}

func main() {
	parseOpts()
	// Trap sigusr to display stats
	go trap()
	if interval != 0 {
		// Display periodicaly.
		go tickDisplay(interval)
	}
	// clean process infos map every 5min
	go tickCPIs(5 * 60 * time.Second)
	// Create Netlink socksts.
	err := initNetlink()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
	}
	// Init cpu counters for all current processes (to get long lived ones).
	updateLongLivedStats(true)
	// Infinite wait for exit events.
	err = getExitStats()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
	}
}
