// Package agent is what runs on the router: a sampler that reads /proc at a
// fixed rate into a ring buffer, and an HTTP server on the veth that hands
// the ring out as NDJSON and as Prometheus text. It links only procfs,
// sample and the standard library. The agent is pull-only: it makes no
// outbound connection and holds no credential of its own beyond the optional
// bearer token a scraper presents. Every credential the tool uses lives on
// the collector host, never in the router's container envlist.
package agent

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config comes from the container envlist. Invalid values fail fast at
// start with one line on stdout, which RouterOS puts in its log.
type Config struct {
	RateHz  int // 1–100 (FromEnv)
	BufferS int // ring buffer seconds
	// Addr is the bind address. install always writes ADDR=<veth address>, so
	// a deployed agent listens on the veth only; with ADDR unset the bind is
	// ":PORT", every address in the container's netns.
	Addr       string
	Port       int
	Token      string // when set, every path but /healthz requires it
	IRQTopK    int
	ProcRoot   string   // "/proc" unless testing
	SysRoot    string   // "/sys" unless testing
	Sources    []string // subset of source names to enable; empty = all detected
	MemLimitMB int      // Go soft memory limit; keeps the heap under the container's memory-max
	FloorHz    int      // global override of every per-source sampling floor, in Hz; 0 = the measured per-source floors
	// Triggered capture (capture.go): the conditions, the pinned-bytes
	// budget in MiB (0 = off), the window around a fire, the full-budget
	// policy and the per-condition refractory window.
	Triggers      string
	CaptureMB     int
	CapturePreS   int
	CapturePostS  int
	CapturePolicy string
	RefractoryS   int
}

// FromEnv reads the documented environment variables, which on the router
// arrive through the container's envlist.
func FromEnv(getenv func(string) string) (Config, error) {
	c := Config{
		RateHz: 10, BufferS: 60, Addr: "", Port: 9123, IRQTopK: 8, ProcRoot: "/proc", SysRoot: "/sys", MemLimitMB: 14,
		Triggers: DefaultTriggers, CaptureMB: 4, CapturePreS: 5, CapturePostS: 5, CapturePolicy: "first", RefractoryS: 10,
	}
	var err error
	intVar := func(name string, dst *int, lo, hi int) {
		if err != nil {
			return
		}
		v := getenv(name)
		if v == "" {
			return
		}
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < lo || n > hi {
			err = fmt.Errorf("%s=%q: want an integer in %d..%d", name, v, lo, hi)
			return
		}
		*dst = n
	}
	intVar("RATE_HZ", &c.RateHz, 1, 100)
	intVar("BUFFER_S", &c.BufferS, 10, 3600)
	intVar("PORT", &c.Port, 1, 65535)
	intVar("IRQ_TOP_K", &c.IRQTopK, 0, 64)
	intVar("MEM_LIMIT_MB", &c.MemLimitMB, 8, 1024)
	// FLOOR_HZ overrides every per-source floor at once. The floors were
	// measured on one device on one idle night, so they are a starting point,
	// not a law: set FLOOR_HZ to the sampler rate to read and emit every source
	// every tick and measure the floors again on another device or workload.
	intVar("FLOOR_HZ", &c.FloorHz, 0, 1000)
	intVar("CAPTURE_MB", &c.CaptureMB, 0, 256)
	intVar("CAPTURE_PRE_S", &c.CapturePreS, 1, 60)
	intVar("CAPTURE_POST_S", &c.CapturePostS, 1, 60)
	intVar("TRIGGER_REFRACTORY_S", &c.RefractoryS, 0, 3600)
	if err != nil {
		return Config{}, err
	}
	if t := getenv("TRIGGERS"); t != "" {
		if _, perr := ParseTriggers(t); perr != nil {
			return Config{}, perr
		}
		c.Triggers = t
	}
	if pol := getenv("CAPTURE_POLICY"); pol != "" {
		if pol != "first" && pol != "last" {
			return Config{}, fmt.Errorf("CAPTURE_POLICY=%q: want first or last", pol)
		}
		c.CapturePolicy = pol
	}
	c.Addr = getenv("ADDR")
	c.Token = getenv("TOKEN")
	if s := getenv("SOURCES"); s != "" {
		for name := range strings.SplitSeq(s, ",") {
			c.Sources = append(c.Sources, strings.TrimSpace(name))
		}
	}
	if r := getenv("PROC_ROOT"); r != "" {
		c.ProcRoot = r
	}
	if r := getenv("SYS_ROOT"); r != "" {
		c.SysRoot = r
	}
	return c, nil
}

// Getenv is os.Getenv, named so main can pass it.
func Getenv(k string) string { return os.Getenv(k) }
