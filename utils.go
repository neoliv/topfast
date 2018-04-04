package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

var wColNb, wRowNb uint

func init() {
	wColNb, wRowNb = getTermDimensions()
}

func getTermDimensions() (col, row uint) {
	ws := &winsize{}
	retCode, _, _ := syscall.Syscall(syscall.SYS_IOCTL, uintptr(syscall.Stdin), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(ws)))
	if int(retCode) == -1 {
		col = 80
		row = 40
		//panic(errno)
	} else {
		col = uint(ws.Col)
		row = uint(ws.Row)
	}
	return
}

// display a separator with an insert.
func printSep(f *os.File, format string, a ...interface{}) {
	lm := 5 // left margin
	sc := "-"
	i := fmt.Sprintf(format, a...)
	if raw {
		fmt.Fprintf(out, "#	%s %s\n", strings.Repeat(sc, lm), i)
		return
	}
	w := int(wColNb) // Separator width will match terminal width.
	li := len(i)
	ri := w - li - lm
	if ri < 0 {
		// two lines (the sep then the insert)
		fmt.Fprintf(out, "%s\n%s\n", strings.Repeat(sc, w), i)
	} else {
		// one line with the insert in the sep
		fmt.Fprintf(out, "%s%s%s\n", strings.Repeat(sc, lm), i, strings.Repeat(sc, ri))
	}
}

var traceOn = true

func tron() {
	traceOn = true
}

func troff() {
	traceOn = false
}

var readBuffer [2048]byte // Used as temp storage for content of files in /proc/[PID]. stat and statm are short files (<400 bytes).

// fastRead reads a short file using a single static common buffer.
// WARNING: Desigend to be fast but has absolutly no guards against concurrent use or big files. (single common  small buffer with no mutex)
func fastRead(fn string) ([]byte, error) {
	//traceCaller(3, "fr: %s", fn)
	// TODO build a special cgo call to open and bypass the need to allocate the path name?
	// give an int to C and use a char[] buffer in C?
	f, err := os.OpenFile(fn, syscall.O_RDONLY, 0) // 	Skip the Open() call.
	if err != nil {
		//trace("open err=%s", err)
		//fmt.Printf("fr: open failed: %s\n", fn)
		return nil, err
	}
	n, err := f.Read(readBuffer[:])
	f.Close()
	if err != nil && err != io.EOF {
		//fmt.Printf("fr: read failed: %s\n", fn)
		return nil, err
	}
	//fmt.Printf("fr: read ok: %s\n", fn)
	return readBuffer[0:n], nil
}

// WARNING: to be fast this function assumes that we are on the first digit of the integer to parse.
func fastParseInt(s []byte, i int) (res int64, index int) {
	res = 0
	sl := len(s)
	for ; i < sl; i++ {
		if s[i] < '0' || '9' < s[i] {
			return res, i
		}
		// TODO bench and find faster algo? (precomputed tables?)
		res = 10*res + int64(s[i]-'0')
	}
	return res, i
}

// Search s for the previous integer.
// Assume we are scaning a file structured like /proc/[pid]/status where we have lines like: key : value.
//In this case we assume i is pointing between the EOL and last digit of the interger and value is an int followed by an optional unit. eg: VmSwap:	     384 kB
func fastParsePrevInt(s []byte, i int) (res int64) {
	m := int64(1)
	j := i
	for ; j >= 0; j-- { // search backward for a digit.
		c := s[j]
		switch {
		case '0' <= c && c <= '9':
			break
		case c == ':':
			return res // should not happen, only guards against anomalous file
		}
	}
	for ; j >= 0; j-- {
		c := s[i]
		if c < '0' || '9' < c {
			return res
		}
		res += m * int64(c-'0')
		m *= 10
	}
	return res
}

// fastParseUntil returns the string between current position and first occurence of delim (delim not included) or end of string. index points on the delim.
func fastParseUntil(s []byte, i int, delim byte) (res string, index int) {
	si := i
	sl := len(s)
	for ; i < sl; i++ {
		if s[i] == delim {
			return string(s[si:i]), i
		}
	}
	return string(s[si : i-1]), i
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func findNextIndex(s []byte, start int, char byte) int {
	sl := len(s)
	for ; start < sl; start++ {
		if s[start] == char {
			return start
		}
	}
	return sl
}

// UInt64Slice attaches the methods of Interface to []int, sorting in increasing order.
type UInt64Slice []uint64

func (p UInt64Slice) Len() int           { return len(p) }
func (p UInt64Slice) Less(i, j int) bool { return p[i] < p[j] }
func (p UInt64Slice) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }

// Sort is a convenience method.
func (p UInt64Slice) Sort() { sort.Sort(p) }

type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}
