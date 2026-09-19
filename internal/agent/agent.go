package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"
)

// Run wires config → source → sampler → HTTP and blocks until ctx is done,
// then shuts the server down within the 10 s RouterOS grants after SIGTERM.
// Measured on the reference RB5009 (RouterOS 7.24.2) on 2026-09-11:
// /container/stop sends SIGTERM and kills at once when the process does not
// catch it, so that 10 s stop-time is a grace only for a process with a
// handler. log receives one line per lifecycle event.
func Run(ctx context.Context, cfg Config, version string, log func(string)) error {
	src, err := NewProcSource(cfg.ProcRoot, cfg.SysRoot, cfg.Sources)
	if err != nil {
		return err
	}
	src.SetFloors(cfg.RateHz)
	src.SetFloorOverride(cfg.FloorHz, cfg.RateHz)
	defer src.Close()
	warn, budgetErr := checkRingBudget(cfg.RateHz, cfg.BufferS, cfg.MemLimitMB, cfg.CaptureMB, src.limits.CgroupMemoryMaxBytes)
	if budgetErr != nil {
		src.Close()
		return budgetErr
	}
	if warn != "" {
		log(warn)
	}
	return RunWith(ctx, cfg, src, version, log)
}

// ApproxLineBytes is what one ring entry costs. Not the line's length: the
// size class the allocator rounds it up to, because that is what the heap is
// actually charged.
//
// MEASURED on the reference RB5009 (RouterOS 7.24.2, 4 cores, K=8 interrupts,
// privileged, every source on) on 2026-09-17: one line is 3 230 B, which Go's
// allocator serves from the 3 456 B size class. The old figure here was
// 2 560 B, taken from a 2 439 B line on 2026-09-12 before the PMU and the
// sampler's own timing were in it, and it understated the ring by 35 %: a
// budget check that passed on paper let the default memory limit sit so far
// above the real heap that it never bound, and the agent ran at 32.9 MiB of
// RSS where 24.3 was available for the asking.
//
// It is still an estimate and still device-dependent — a board with more
// cores or more interrupt lines pays more per line, one with no PMU pays 35 %
// less, and a line one byte either side of a size class boundary moves by a
// whole class. The check errs open on the small side and the operator can
// always read the real figure: it is the length of any line /snapshot serves.
const ApproxLineBytes = 3456

// checkRingBudget refuses a ring that cannot fit, and warns about one that
// will make the collector thrash.
//
// Nothing validated this until 2026-09-15: RATE_HZ and BUFFER_S are each
// range-checked, but their PRODUCT is the ring, and the documented maxima
// (100 Hz x 300 s) give 69.8 MB against install's 64 M memory-max — the OOM
// killer, not an error message. BUFFER_S allows 3600. The arithmetic is done
// in int64 on purpose: on the 32-bit hEX S an int is 32 bits and 100 x 3600
// x 2560 overflows it.
//
// The second half is a measured lesson. On the reference RB5009 (RouterOS
// 7.24.2) on 2026-09-12, a ring the Go soft limit could not hold twice over
// put the garbage collector in permanent overtime: 9.38 % of one core, where
// the same workload with headroom cost 1.39 %. The warning fires at that
// ratio. Both figures are steady state with the ring full, read from
// mikroscope_self_cpu_usec_total; the first minute after start says something
// else entirely.
func checkRingBudget(rateHz, bufferS, memLimitMB, captureMB int, cgroupMax uint64) (warn string, err error) {
	// The capture budget pins ring bytes beyond the ring's steady state, so
	// it counts against the same ceilings.
	ringBytes := int64(rateHz)*int64(bufferS)*ApproxLineBytes + int64(captureMB)<<20
	if cgroupMax > 0 && ringBytes > int64(cgroupMax) { // #nosec G115 -- a memory size fits int64
		return "", fmt.Errorf("ring of %d Hz x %d s plus %d MiB of captures needs about %d MiB but the container's memory.max is %d MiB: lower RATE_HZ, BUFFER_S or CAPTURE_MB, or raise --memory-max",
			rateHz, bufferS, captureMB, ringBytes>>20, cgroupMax>>20)
	}
	if memLimitMB > 0 && ringBytes*2 > int64(memLimitMB)<<20 {
		warn = fmt.Sprintf("warning: ring of %d Hz x %d s is about %d MiB, more than half the %d MiB memory limit; the garbage collector will run continuously. Raise --mem-limit-mb to at least %d",
			rateHz, bufferS, ringBytes>>20, memLimitMB, (ringBytes*2>>20)+1)
	}
	return warn, nil
}

// RunWith is Run with an injected source, for tests.
func RunWith(ctx context.Context, cfg Config, src Source, version string, log func(string)) error {
	if cfg.MemLimitMB > 0 {
		// Without a limit the collector lets the heap reach twice the live
		// set before collecting; with 6 000 ring entries live that met the
		// container's memory-max on the RB5009 (32 MB at 20 Hz, 2026-09-11).
		debug.SetMemoryLimit(int64(cfg.MemLimitMB) << 20)
	}
	caps := src.Capabilities()
	ring := NewRing(cfg.RateHz * cfg.BufferS)
	sampler := NewSampler(src, ring, cfg.RateHz, cfg.IRQTopK)
	conds, condErr := ParseTriggers(cfg.Triggers)
	if condErr != nil {
		return condErr
	}
	captures := NewCaptures(CaptureConfig{
		Conditions: conds, RateHz: cfg.RateHz, PreS: cfg.CapturePreS, PostS: cfg.CapturePostS,
		Budget: int64(cfg.CaptureMB) << 20, Policy: cfg.CapturePolicy, Refractory: cfg.RefractoryS,
	})
	sampler.SetCaptures(captures)
	srv := &Server{Ring: ring, Sampler: sampler, Caps: caps, Captures: captures, Token: cfg.Token, RateHz: cfg.RateHz, Version: version, Start: time.Now()}
	addr := net.JoinHostPort(cfg.Addr, strconv.Itoa(cfg.Port))
	// WriteTimeout bounds a scraper that stops reading. Render no longer
	// holds the totals mutex while writing, so a stalled client can no longer
	// block the sampler — but it could still pin a goroutine and a buffer
	// forever; this is the second half of that fix.
	hs := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	log(fmt.Sprintf("mikroscope-agent %s: kernel %s, %d cores, %d Hz, ring %d s, listening on %s, sources %v, token=%v, captures %d MiB triggers=%q",
		version, caps.Kernel, caps.Cores, cfg.RateHz, cfg.BufferS, ln.Addr(), enabledNames(caps), cfg.Token != "", cfg.CaptureMB, cfg.Triggers))

	errc := make(chan error, 2)
	go func() { errc <- sampler.Run(ctx) }()
	go func() {
		if serveErr := hs.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errc <- serveErr
		}
	}()
	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errc:
	}
	// ctx is done (or the run failed): the shutdown needs a deadline of its
	// own, detached from the canceled parent.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(shutdownCtx)
	log(fmt.Sprintf("mikroscope-agent: stopping after %d samples, %d slipped", sampler.Ticks(), sampler.Slipped()))
	return runErr
}

func enabledNames(c Capabilities) []string {
	var out []string
	for _, n := range []string{"stat", "meminfo", "loadavg", "softnet", "softirqs", "interrupts", "vmstat", "psi", "schedstat", "self"} {
		if c.Sources[n] {
			out = append(out, n)
		}
	}
	return out
}
