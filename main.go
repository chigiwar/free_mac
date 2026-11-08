//go:build darwin
// +build darwin

// Command free is a macOS port of the Linux util-linux `free(1)` command.
//
// It reports memory usage with the same columns and almost the same
// command-line options as the Linux original. The -l/--lohi flag is not
// applicable to macOS and is accepted but ignored.
//
// Memory counters come from Mach's host_statistics64() (HOST_VM_INFO64)
// and swap figures from sysctl("vm.swapusage"), so the tool does not
// depend on /proc, which does not exist on macOS.
//
// Build on macOS (cgo is enabled by default and requires the Xcode
// Command Line Tools):
//
//	go build -o free free.go
//
// Column mapping to the Linux original:
//
//	total       hw.memsize
//	used        app memory (internal + compressed pages) + wired pages
//	free        free pages
//	shared      0 (macOS does not report tmpfs-style shared memory here)
//	buff/cache  external (file-backed) pages
//	available   free + buff/cache (approximation of Linux's MemAvailable)
package main

/*
#include <mach/mach.h>
#include <sys/sysctl.h>
#include <stdint.h>
#include <string.h>

struct free_xsw_usage {
	uint64_t xsu_total;
	uint64_t xsu_used;
	uint64_t xsu_avail;
	uint32_t xsu_encrypted;
	uint32_t xsu_reserved;
};

static int free_vm_stats(vm_statistics64_data_t *out)
{
	mach_msg_type_number_t count = HOST_VM_INFO64_COUNT;
	return host_statistics64(mach_host_self(), HOST_VM_INFO64,
	                         (host_info64_t)out, &count);
}

static uint64_t free_memsize(void)
{
	uint64_t v = 0;
	size_t sz = sizeof(v);
	if (sysctlbyname("hw.memsize", &v, &sz, NULL, 0) != 0)
		return 0;
	return v;
}

static uint64_t free_pagesize(void)
{
	vm_size_t ps = 4096;
	if (host_page_size(mach_host_self(), &ps) != KERN_SUCCESS || ps == 0)
		return 4096;
	return (uint64_t)ps;
}

static int free_swap(uint64_t *total, uint64_t *used, uint64_t *avail)
{
	struct free_xsw_usage xu;
	size_t sz = sizeof(xu);
	memset(&xu, 0, sizeof(xu));
	if (sysctlbyname("vm.swapusage", &xu, &sz, NULL, 0) != 0)
		return -1;
	*total = xu.xsu_total;
	*used  = xu.xsu_used;
	*avail = xu.xsu_avail;
	return 0;
}
*/
import "C"

import (
	"flag"
	"fmt"
	"os"
	"time"
)

const version = "free (macOS port of util-linux free) 1.0.0"

type options struct {
	divisor   uint64 // unit divisor in bytes (default 1024, like free's KiB)
	human     bool
	si        bool // powers of 1000 instead of 1024
	wide      bool
	showTotal bool
	delay     float64 // seconds; 0 = print once
	count     int     // 0 = unlimited (only meaningful with delay)
}

type memInfo struct {
	total   uint64
	used    uint64
	free    uint64
	shared  uint64
	cache   uint64
	avail   uint64
	swapTot uint64
	swapUse uint64
	swapFre uint64
	hasSwap bool
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "free: "+msg)
	os.Exit(1)
}

func readMem() memInfo {
	var vm C.vm_statistics64_data_t
	if C.free_vm_stats(&vm) != C.KERN_SUCCESS {
		fatal("host_statistics64(HOST_VM_INFO64) failed")
	}
	ps := uint64(C.free_pagesize())

	mi := memInfo{total: uint64(C.free_memsize())}
	if mi.total == 0 {
		fatal("hw.memsize sysctl failed")
	}

	// "App memory" in Activity Monitor terms: anonymous (internal) pages
	// plus pages occupied by the compressor, plus wired kernel memory.
	app := (uint64(vm.internal_page_count) + uint64(vm.compressor_page_count)) * ps
	wired := uint64(vm.wire_count) * ps

	mi.used = app + wired
	mi.free = uint64(vm.free_count) * ps
	mi.cache = uint64(vm.external_page_count) * ps // file-backed pages
	mi.shared = 0
	mi.avail = mi.free + mi.cache

	var t, u, a C.uint64_t
	if C.free_swap(&t, &u, &a) == 0 {
		mi.hasSwap = true
		mi.swapTot, mi.swapUse, mi.swapFre = uint64(t), uint64(u), uint64(a)
	}
	return mi
}

func humanSize(v uint64, si bool) string {
	base := 1024.0
	if si {
		base = 1000.0
	}
	units := "BKMGTPE"
	f := float64(v)
	i := 0
	for f >= base && i < len(units)-1 {
		f /= base
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%dB", v)
	}
	if f >= 100 {
		return fmt.Sprintf("%.0f%c", f, units[i])
	}
	return fmt.Sprintf("%.1f%c", f, units[i])
}

func printSnapshot(mi memInfo, o options) {
	f := func(v uint64) string {
		if o.human {
			return humanSize(v, o.si)
		}
		return fmt.Sprintf("%d", v/o.divisor)
	}
	dash := "-"
	pad := func(vals []string, n int) []string {
		for len(vals) < n {
			vals = append(vals, dash)
		}
		return vals
	}

	headers := []string{"total", "used", "free", "shared"}
	if o.wide {
		headers = append(headers, "buffers", "cache")
	} else {
		headers = append(headers, "buff/cache")
	}
	headers = append(headers, "available")

	memVals := []string{f(mi.total), f(mi.used), f(mi.free), f(mi.shared)}
	if o.wide {
		memVals = append(memVals, dash, f(mi.cache))
	} else {
		memVals = append(memVals, f(mi.cache))
	}
	memVals = append(memVals, f(mi.avail))

	rows := [][]string{append([]string{"Mem:"}, memVals...)}
	if mi.hasSwap {
		rows = append(rows, append([]string{"Swap:"},
			pad([]string{f(mi.swapTot), f(mi.swapUse), f(mi.swapFre)}, len(headers))...))
	}
	if o.showTotal {
		rows = append(rows, append([]string{"Total:"},
			pad([]string{f(mi.total + mi.swapTot), f(mi.used + mi.swapUse), f(mi.free + mi.swapFre)}, len(headers))...))
	}

	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, r := range rows {
		for i, c := range r[1:] {
			if len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}

	fmt.Print("       ")
	for i, h := range headers {
		fmt.Printf("%*s ", widths[i], h)
	}
	fmt.Println()
	for _, r := range rows {
		fmt.Printf("%-7s", r[0])
		for i, c := range r[1:] {
			fmt.Printf("%*s ", widths[i], c)
		}
		fmt.Println()
	}
}

func parseFlags() options {
	fs := flag.NewFlagSet("free", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	boolOpt := func(short, long, usage string) *bool {
		p := new(bool)
		if short != "" {
			fs.BoolVar(p, short, false, usage)
		}
		if long != "" {
			fs.BoolVar(p, long, false, usage)
		}
		return p
	}

	optB := boolOpt("b", "bytes", "show output in bytes")
	optK := boolOpt("k", "kibi", "show output in kibibytes (default)")
	optM := boolOpt("m", "mebi", "show output in mebibytes")
	optG := boolOpt("g", "gibi", "show output in gibibytes")
	optTera := boolOpt("", "tera", "show output in tebibytes")
	optHuman := boolOpt("h", "human", "show human-readable output")
	optSI := boolOpt("", "si", "use powers of 1000, not 1024")
	optWide := boolOpt("w", "wide", "wide output, separate buffers and cache columns")
	optTotal := boolOpt("t", "total", "show total for RAM + swap")
	optLohi := boolOpt("l", "lohi", "ignored on macOS (no low/high memory split)")
	optHelp := boolOpt("", "help", "display this help and exit")
	optVersion := boolOpt("", "version", "display version information and exit")

	var delay float64
	var count int
	fs.Float64Var(&delay, "s", 0, "repeat continuously every N seconds")
	fs.Float64Var(&delay, "seconds", 0, "see -s")
	fs.IntVar(&count, "c", 0, "repeat N times, then exit (implies -s 1)")
	fs.IntVar(&count, "count", 0, "see -c")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `free - Display amount of free and used memory in the system

Usage:
 free [options]

Options:
  -b, --bytes      show output in bytes
  -k, --kibi       show output in kibibytes (default)
  -m, --mebi       show output in mebibytes
  -g, --gibi       show output in gibibytes
      --tera       show output in tebibytes
  -h, --human      show human-readable output
      --si         use powers of 1000, not 1024
  -w, --wide       wide output, separate buffers and cache columns
  -t, --total      show total for RAM + swap
  -s, --seconds N  repeat continuously every N seconds
  -c, --count N    repeat N times, then exit
  -l, --lohi       (ignored on macOS)
      --help       display this help and exit
      --version    display version information and exit
`)
	}

	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if *optHelp {
		fs.Usage()
		os.Exit(0)
	}
	if *optVersion {
		fmt.Println(version)
		os.Exit(0)
	}
	if fs.NArg() > 0 {
		fs.Usage()
		os.Exit(2)
	}
	_ = optLohi // accepted for compatibility, not applicable on macOS

	o := options{
		divisor:   1024,
		human:     *optHuman,
		si:        *optSI,
		wide:      *optWide,
		showTotal: *optTotal,
		delay:     delay,
		count:     count,
	}

	// Unit: if several unit flags were given, the last one on the
	// command line wins (same behaviour as the Linux tool).
	divMap := map[string]uint64{
		"b": 1, "bytes": 1,
		"k": 1024, "kibi": 1024,
		"m": 1 << 20, "mebi": 1 << 20,
		"g": 1 << 30, "gibi": 1 << 30,
		"tera": 1 << 40,
	}
	fs.Visit(func(fl *flag.Flag) {
		if d, ok := divMap[fl.Name]; ok {
			o.divisor = d
		}
	})
	_ = optB
	_ = optK
	_ = optM
	_ = optG
	_ = optTera
	return o
}

func main() {
	o := parseFlags()

	// Like procps-ng free: -c without -s repeats once per second.
	if o.count > 0 && o.delay <= 0 {
		o.delay = 1
	}

	for i := 0; ; i++ {
		printSnapshot(readMem(), o)
		if (o.count > 0 && i+1 >= o.count) || o.delay <= 0 {
			break
		}
		time.Sleep(time.Duration(o.delay * float64(time.Second)))
	}
}
