package main

/* Some general tips to understand this code.
* Use netlink to get stats about all ending processes.
* Two maps. One per PID, one per command.
* Any dead process will trigger a walk up its list of ancestors (using ppid fields). All ancestors will be credited this process resource usage.
 */

/*
#include "nlstats.c"
*/
import "C"

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const (
	scCount = iota
	scTime  = iota
)

var scStrings = [2]string{}
var sortCriteria int
var vanishedCount uint64   // number of failed read in /proc/#/stat == vanished proces count.
var removedCount uint64    // how many removed processes.
var exitCount uint64       // how many exit events send from kernel.
var sessionStart time.Time // This process start time.
var sampleStart time.Time  // Current sample start time.
var ehist = [32]uint64{}   // execution time histogram
var sample uint = 0        // number of samples done.
var display uint = 0       // number of displays done.

type cmdInfo struct {
	cmd   string // command
	subec uint64 // count how many sub processes this command has owned (all descendents)
	subet uint64 // sum of execution time in all sub processes. [in us]
	ec    uint64 // number of times this command has been seedn.
	et    uint64 // sum of exec time in all instances of this command. [in us]
	spid  int    // source pid of the last tree walk up that updated sub*
}

type procInfo struct {
	pid  int       // this process PID
	ppid int       // parent PID
	ppi  *procInfo // Parent process info.
	ci   *cmdInfo  // Info about all processes sharing this command.
	cpu  uint64    // cpu exec time since start of process (in us)
}

var mutInfos = sync.Mutex{} // protect the *info maps

// For every command stores its ifnormations.
var cmdInfos = map[string](*cmdInfo){}

// For every PID stores its informations.
var procInfos = map[int](*procInfo){}

func init() {
	sessionStart = time.Now()
	sampleStart = sessionStart
	sortCriteria = scTime
	scStrings[scCount] = "number of exit"
	scStrings[scTime] = "execution time"
}

// Reset all counters for a new sample (like a fresh start).
func clearCounters() {
	sample++
	//fmt.Printf("clearCounters %d\n", sample)
	procInfos = map[int](*procInfo){}
	cmdInfos = map[string](*cmdInfo){}
	if hist == true {
		ehist = [32]uint64{} // execution time histogram
	}
	sampleStart = time.Now()
	updateLongLivedStats(true) // Reset cpu counters for long lived processes.
}

// Display the per command stats.
func statsByCommand(ts int64, dts, dtus float64) {
	if raw {
		if display == 0 {
			fmt.Fprintf(out, "## top %d commands sorted by %s\n", top, scStrings[sortCriteria])
			fmt.Fprintf(out, "## [time stamp s]:cmd:[command]:[CPU percent]:[time usec]:[nb exec percent]:[nb exec per s]\n")
		}
	} else {
		printSep(out, " top %d commands sorted by %s ", top, scStrings[sortCriteria])
	}
	n := map[uint64][](*cmdInfo){}
	var a UInt64Slice
	mutInfos.Lock()
	for _, ci := range cmdInfos {
		var ui uint64
		switch sortCriteria {
		case scCount:
			ui = ci.ec
		case scTime:
			ui = ci.et
		}
		if ui != 0 {
			n[ui] = append(n[ui], ci)
		}
	}
	mutInfos.Unlock()
	for k := range n {
		a = append(a, k)
	}
	sort.Sort(sort.Reverse(a))
	// Compute sum of values (required to get percents)
	var sec, set uint64 // sum of ec/et
	var i int
	for _, k := range a {
		for _, ci := range n[k] {
			sec += ci.ec
			set += ci.et
		}
	}
	// Display sorted stats.
	for _, k := range a {
		for _, ci := range n[k] {
			cmd := ci.cmd
			if cmd == "" {
				cmd = "(vanished)"
			}
			ec := ci.ec
			ecpc := ((float64(ec) * 100) / float64(sec))
			eps := (float64(ec) / dts)
			et := ci.et // *et in usec (microseconds 1e-6)
			etpc := cpuPercent(float64(et), dtus)
			var det = time.Duration(et * 1e3) // Duration is in ns
			if raw {
				fmt.Fprintf(out, "%d:cmd:%s:%.2f:%d:%.2f:%d:%f\n", ts, cmd, etpc, et, ecpc, ec, float64(ec)/dts)
			} else {
				switch sortCriteria {
				case scCount:
					fmt.Fprintf(out, "%15s: %.2f%%ec (%d) %.2fe/s   %.2f%%et (%s)\n", cmd, ecpc, ec, eps, etpc, det.String())
				case scTime:
					fmt.Fprintf(out, "%15s: %.2f%%et (%s)   %.2f%%ec (%d) %.2fe/s\n", cmd, etpc, det.String(), ecpc, ec, eps)
				}
			}

			i++
			if i > top {
				return
			}
		}
	}
}

// Display stats about the comamnd and all its subprocesses (the whole tree).
func statsSub(ts int64, dts, dtus float64) {
	if raw {
		if display == 0 {
			fmt.Fprintf(out, "## top %d commands sorted by sum of subprocesses %s\n", top, scStrings[sortCriteria])
			fmt.Fprintf(out, "## [time stamp s]:sub:[command]:[CPU percent]:[time usec]:[nb exec percent]:[nb exec per s]\n")
		}
	} else {
		printSep(out, " top %d commands sorted by sum of subprocesses %s ", top, scStrings[sortCriteria])
	}
	n := map[uint64][](*cmdInfo){}
	var a UInt64Slice
	mutInfos.Lock()
	for _, ci := range cmdInfos {
		if ci.subec == 0 || ci.cmd == "" || ci.cmd == "init" || ci.cmd == "systemd" {
			// No sub processes or we know that every process is sub of init, no need to mess stats with this one.
			continue
		}
		var ui uint64
		switch sortCriteria {
		case scCount:
			ui = ci.subec
		case scTime:
			ui = ci.subet
		}
		if ui != 0 {
			n[ui] = append(n[ui], ci)
		}
	}
	mutInfos.Unlock()
	for k := range n {
		a = append(a, k)
	}
	sort.Sort(sort.Reverse(a))
	// Compute sum of values (required to get percents)
	var ssubec, ssubet uint64 // sum of ec/et
	var i int
	for _, k := range a {
		for _, ci := range n[k] {
			ssubec += ci.subec
			ssubet += ci.subet
		}
	}
	// Display sorted stats.
	for _, k := range a {
		for _, ci := range n[k] {
			cmd := ci.cmd
			if cmd == "" {
				cmd = "(vanished)"
			}
			subec := ci.subec
			subecpc := ((float64(subec) * 100) / float64(ssubec))
			subeps := (float64(subec) / dts)
			subet := ci.subet // *et in usec (microseconds 1e-6)
			subetpc := cpuPercent(float64(subet), dtus)
			var det = time.Duration(subet)
			if raw {
				fmt.Fprintf(out, "%d:sub:%s:%.2f:%d:%.2f:%d:%f\n", ts, cmd, subetpc, subet, subecpc, subec, float64(subec)/dts)
			} else {
				switch sortCriteria {
				case scCount:
					fmt.Fprintf(out, "%15s: %.2f%%ec (%d) %.2fe/s   %.2f%%et (%s)\n", cmd, subecpc, subec, subeps, subetpc, det.String())
				case scTime:
					fmt.Fprintf(out, "%15s: %.2f%%et (%s)   %.2f%%ec (%d) %.2fe/s\n", cmd, subetpc, det.String(), subecpc, subec, subeps)
				}
			}
			i++
			if i > top {
				return
			}
		}
	}
}

// Display the histogram for command execution time.
func statsEHist(dts, dtus float64) {
	var firsti, lasti int
	var s uint64 // sum of all values in the histogram.
	firsti = -1
	for l := 0; l < len(ehist); l++ {
		if ehist[l] != 0 {
			lasti = l
			s += ehist[l]
			if firsti < 0 {
				firsti = l // index of the first non 0 sample
			}
		}
	}
	if firsti < 0 {
		// nothing in the histogram, skip its display.
		return
	}
	printSep(out, " command execution time histogram (%d executed commands) ", exitCount)
	fmt.Fprintf(out, "|")
	p := 1
	for l := 0; l <= lasti; l++ {
		p *= 10
		if l >= firsti {
			fmt.Fprintf(out, " <%5s |", time.Duration(p).String())
		}
	}
	fmt.Fprintf(out, "\n|")
	for l := firsti; l <= lasti; l++ {
		if ehist[l] != 0 {
			p5 := math.Ceil(float64(10000*ehist[l]) / float64(s))
			pc := p5 / 100
			pcs := strconv.FormatFloat(pc, 'f', -1, 64)
			//pcs := fmt.Sprintf("%4f", pc)
			fmt.Fprintf(out, "%6s%% |", pcs)
		} else {
			fmt.Fprintf(out, "        |")

		}
	}
	fmt.Fprintf(out, "\n")
}

// Display a summary of gathered stats.
func stats() {
	// First update stats about all long lived processes.
	getTermDimensions() // Update the term width every display.
	t := time.Now().Unix()
	dt := time.Since(sampleStart)
	updateLongLivedStats(false) // Get cpu usage for long lived processes since last sample.
	dts := dt.Seconds()
	dtus := dts * 1e6 // us is mucriseconds 1e-6
	var pref string
	if !raw {
		pref = ""
	} else {
		pref = "# " // in raw mode we prefix the header lines with #
	}
	hn, _ := os.Hostname()
	fmt.Fprintf(out, "%shostname:           %s\n", pref, hn)
	fmt.Fprintf(out, "%sdate:               %s\n", pref, time.Now())
	fmt.Fprintf(out, "%scpus:               %d\n", pref, cpuNb)
	fmt.Fprintf(out, "%ssample duration:    %s\n", pref, time.Duration.String(dt))
	fmt.Fprintf(out, "%sexit count:         %d (%.2fe/s)\n", pref, exitCount, float32(exitCount)/float32(dts))
	fmt.Fprintf(out, "%snumber of comamnds: %d\n", pref, len(cmdInfos))

	if top > 0 {
		statsByCommand(t, dts, dtus)
	}
	if !raw && hist {
		statsEHist(dts, dtus)
	}
	if top > 0 {
		statsSub(t, dts, dtus)
	}
	printSep(out, "")
	display++
}

// readProcStats Extract the command (and ppid) from /proc/[pid]/stat
func readProcStat(pid int) (string, int) {
	fn := fmt.Sprintf("/proc/%d/stat", pid)
	s, err := fastRead(fn)
	sl := len(s)
	if err != nil || sl == 0 {
		vanishedCount++
		return "", -1
	}
	var f int // field number (0 is pid)
	var i64 int64
	var cmd string
	for i := 0; i < sl; i++ {
		//fmt.Fprintf(out,"f:%d i:%d c:%c\n", f, i, s[i])
		switch f {
		case 1: // 1 tcomm
			i++ // Skip the '('.
			cmd, i = fastParseUntil(s, i, ')')
		case 3: // 3 ppid
			i64, i = fastParseInt(s, i)
			return cmd, int(i64)
		default: // Skip this field.
			i++
			for ; i < sl; i++ {
				if s[i] == ' ' {
					break
				}
			}
		}
		// Assume one and only one ' '  between fields.
		f++
	}
	return "", -1
}

// Global to avoid passing to C and back. Does this update phase need to init cpu counters?
// Used only by goUpdateStats() => only in ont thread.
var initCpuCounters bool

// Update stats for long lived processes foundin /proc
func updateLongLivedStats(init bool) error {
	// Get all new proicesses
	d, err := os.Open("/proc/")
	if err != nil {
		return err
	}
	defer d.Close()
	fnames, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, fname := range fnames {
		if (fname[0] < '0') || (fname[0] > '9') {
			// Skip the conversion attempt if we know beforehand this is not a PID.
			continue
		}
		// Probably a PID.
		pid, err := strconv.ParseInt(fname, 10, 32)
		if err != nil {
			// If not numeric name then skip.
			continue
		}
		// This will send a request for stats then read all waiting asnwers.
		// Every answer will trigger a call to GO function goUpdateStats()
		initCpuCounters = init
		C.request_pid_stats(C.__u32(pid))
	}
	return nil
}

// Remove all dead processes from the global procInfos map.
// The exit event callback should handle this but in some cases we may miss events.
func cleanProcInfos() {
	mutInfos.Lock()
	for pid := range procInfos {
		process, _ := os.FindProcess(pid) // On UNIX always success.
		err := process.Signal(syscall.Signal(0))
		if err != nil {
			delete(procInfos, pid)
			removedCount++
		}
	}
	mutInfos.Unlock()
}

// incCmd increment command counters (cpu, execution count) in cmdInfos (create new entry if need be)
func incCmd(ci *cmdInfo, cmd string, et uint64, ec uint64) *cmdInfo {
	if ci == nil {
		ci, _ = cmdInfos[cmd]
	}
	if ci != nil {
		// The command is known.
		ci.ec += ec
		ci.et += et
	} else {
		ci = &cmdInfo{cmd: cmd, et: et, ec: ec}
		cmdInfos[cmd] = ci
	}
	return ci
}

// propagateStats walk up the pid chain and add cpu and execution count to parent commands.
func propagateStats(spid int, pi *procInfo, pid int, et uint64, ec uint64) *procInfo {
	//fmt.Printf("propagateStats: pi:%v pid:%d cpu:%d ec:%d\n", pi, pid, cpu, ec)
	if pid <= 1 {
		// walked up to init process (pid==0)
		return nil
	}
	if pi == nil {
		// Is this PID already known?
		pi, _ = procInfos[pid]
	}
	if pi == nil {
		// First time we see this pid.
		cmd, ppid := readProcStat(pid)
		//fmt.Printf("read /proc %d: %s %d\n", pid, cmd, ppid)
		pi = &procInfo{pid: pid, ppid: ppid}
		procInfos[pid] = pi
		var ci *cmdInfo
		var known bool
		if ci, known = cmdInfos[cmd]; !known {
			ci = &cmdInfo{cmd: cmd}
			cmdInfos[cmd] = ci
		}
		pi.ci = ci
	}
	if pi.ci != nil {
		// We are walking up the ppid chain. The increments are for sub commands.
		if spid != pi.ci.spid {
			pi.ci.subec += ec
			pi.ci.subet += et
			pi.ci.spid = spid
		}
	}
	if pi.ppid != 0 {
		if pi.ppi != nil {
			propagateStats(spid, pi.ppi, pi.ppid, et, ec)
		} else {
			pi.ppi = propagateStats(spid, nil, pi.ppid, et, ec)
		}
	}

	return pi
}

//export goUpdateStats
// This method is called from C every time a process stats is read (after a request for update).
func goUpdateStats(cpid, cppid C.int, ccpu C.ulong, ccmd *C.char) {
	pid := int(cpid)
	ppid := int(cppid)
	cpu := uint64(ccpu)
	cmd := C.GoString(ccmd)
	mutInfos.Lock()
	var det uint64
	pi, known := procInfos[pid]
	if known {
		// Usual case, we request mostly long lived processes so they are already known.
		if initCpuCounters == true {
			// New sample => (re)init cpu counters for all long lived processes.clear
			det = 0
		} else if cpu >= pi.cpu {
			// exec time since last sampling.
			det = cpu - pi.cpu
		} else {
			// very rare overflow. TODO compute the exact overflow not only the wrap around part?
			det = cpu
		}
		pi.ci = incCmd(pi.ci, cmd, det, 0)
		pi.ppi = propagateStats(pid, pi.ppi, ppid, det, 0)
		pi.cpu = cpu // new reference cpu counter.
	} else {
		// First time we see this process.
		pi = &procInfo{pid: pid, ppid: ppid, cpu: cpu}
		procInfos[pid] = pi
		if initCpuCounters == true {
			// (re)init cpu counters for all long lived processes.
			det = 0
		} else {
			det = cpu // this process was not here at the start of the sample. count all its cpu for this sample.
		}
		pi.ci = incCmd(nil, cmd, det, 1)
		pi.ppi = propagateStats(pid, nil, ppid, det, 1)
		pi.cpu = cpu
	}
	mutInfos.Unlock()
}

//export goExitStats
// This method is called from C every time a process exists and sends its stats on the netlink socket.
// cpu is the sum of system and user execution time in usec (from taskstat ac_utime+ac_stime)
func goExitStats(cpid, cppid C.int, ccpu C.ulong, ccmd *C.char) {
	exitCount++
	pid := int(cpid)
	ppid := int(cppid)
	cpu := uint64(ccpu)
	cmd := C.GoString(ccmd)
	//fmt.Fprintf(out, "Exit Stats: pid=%d ppid=%d uid=%d cpu=%d cmd=%s\n", pid, ppid, uid, cpu, cmd)
	// We update histogram only on exit (not on update)
	if ccpu == 0 {
		ccpu++ // avoid log(0)
	}
	if hist == true {
		i := int(math.Log10(float64(ccpu)))
		//fmt.Printf("cpu:%d i:%d\n", ccpu, i)
		ehist[i]++
	}
	mutInfos.Lock()
	if pi, known := procInfos[pid]; !known {
		// Usual case where this exit event is the first time we see this pid.
		incCmd(nil, cmd, cpu, 1)
		propagateStats(pid, nil, ppid, cpu, 1)
	} else {
		// Sometimes we already have created this pid when walking up the ppid chain.
		// TODO handle out of order exits with ungathered stats?
		delete(procInfos, pid)
		incCmd(pi.ci, cmd, cpu, 1)
		propagateStats(pid, pi.ppi, ppid, cpu, 1)
	}
	mutInfos.Unlock()
}

func initNetlink() error {
	// Set a high scheduling priority to give this process to better chances to access /proc/[pid]/stat fast enough once it gets a netlink exec() event.
	syscall.Setpriority(syscall.PRIO_PROCESS, 0, -20)

	// Prepare a netlink socket where we will ask for stats.
	rc := C.init_tgid_stats()
	if rc != 0 {
		return fmt.Errorf("Fatal error with the Netlink socket (%s).\n Remember that you need to have root permissions to use netlink sockets.\n", C.GoString(C.error_msg()))
	}
	// This C function will connect to the kernel and wait for all events.
	// Events will be handled by callbacks in go. (see goProcEvent* functions above(.
	rc = C.init_nlstats()
	if rc != 0 {
		return fmt.Errorf("Fatal error with the Netlink socket (%s).\n Remember that you need to have root permissions to use netlink sockets.\n", C.GoString(C.error_msg()))
	}
	return nil
}

// Get process events directly from the Linux kernel (via tne netlink. No lag, no missed events, ... Far superior to any scan based algorithm but not portable.
func getExitStats() error {
	// Blocking call that will handle netlink events and call back go when a process exit stats are available.
	rc := C.get_exit_stats()
	if rc != 0 {
		return fmt.Errorf("Fatal error with the Netlink socket (%s).\n Remember that you need to have root permissions to use netlink sockets.\n", C.GoString(C.error_msg()))
	}
	return nil
}
