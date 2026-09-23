// Package health reads a window of the agent's samples and names the faults
// it shows: the ones that are happening NOW, in the ring the agent still holds.
//
// It is what `doctor` runs against an installed agent. It is deliberately
// narrow: the ring is sixty seconds by default, so a fault that happens once a
// day is not here and belongs to the dashboards and the alert rules, which
// read history. What is here is what an operator who has just typed `doctor`
// because something feels wrong most needs to be told, and in particular the
// kind of fault RouterOS's own tools do not show.
//
// Every check is written so that it is right on any router rather than tuned
// to one: a count of events that should not happen at all, never a threshold
// on a quantity whose healthy value depends on the device.
package health

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
)

// Finding is one fault the window shows, with what to do about it.
type Finding struct {
	Check  string // short, stable name: layer2-loop, stp-churn, link-flap, softnet-drops
	Port   string // the RouterOS port it concerns, empty when it concerns none
	Detail string // what was seen, with the numbers
	Fix    string // the next thing to try
}

// Report is the window that was read and what it showed.
type Report struct {
	Samples  int
	Seconds  float64
	Findings []Finding
}

// Thresholds, each a count of events that a healthy router does not produce at
// all rather than a rate tuned to one device's traffic.
const (
	// ownAddressMin: one frame coming back with the bridge's own source
	// address can happen once while a cable is being re-plugged. Three in one
	// window is a pattern. The reference RB5009's loop repeated every 2.0 s,
	// the STP hello interval — thirty in a sixty-second ring (2026-09-12 and
	// again 2026-09-19..23).
	ownAddressMin = 3
	// stpStuckMin: how many more times a port entered learning than it went on
	// to forwarding. Blocking is no measure: every healthy link-up logs three
	// blocking records at once and reaches forwarding 2.1–2.8 s later (ether7
	// on the reference RB5009, five link-ups on 2026-09-21). Across 30 days of
	// its store every healthy link-up left learning minus forwarding at exactly
	// 0, and the loop of 2026-09-19..23 left up to 150 per 5 min on ether2.
	stpStuckMin = 3
	// linkDownMin: one link-down is an event — a cable pulled, a device
	// switched off. Two in one window is a port flapping. The same count the
	// collector's link-flap detection uses (internal/derive).
	linkDownMin = 2
)

// Analyze reads the samples the agent's ring held and returns what they show.
//
// The kernel-log records are classified HERE, from their text, with the board
// the agent reported, rather than trusted as the agent annotated them: an
// agent old enough to ship records without a Kind would otherwise make every
// port check pass by saying nothing.
func Analyze(board string, samples []sample.Sample) Report {
	rep := Report{Samples: len(samples)}
	if len(samples) >= 2 {
		rep.Seconds = float64(samples[len(samples)-1].WallNS-samples[0].WallNS) / 1e9
	}

	perPort, dropped, dropCPUs := tally(board, samples)
	window := describeWindow(rep)
	ports := make([]string, 0, len(perPort))
	for p := range perPort {
		ports = append(ports, p)
	}
	sort.Strings(ports)
	for _, port := range ports {
		rep.Findings = append(rep.Findings, portFindings(port, perPort[port], window)...)
	}

	if dropped > 0 {
		cpus := make([]string, 0, len(dropCPUs))
		for cpu := range dropCPUs {
			cpus = append(cpus, fmt.Sprintf("cpu%d", cpu))
		}
		sort.Strings(cpus)
		rep.Findings = append(rep.Findings, Finding{
			Check:  "softnet-drops",
			Detail: fmt.Sprintf("the kernel dropped %d received packets in its softnet backlog %s, on %s", dropped, window, strings.Join(cpus, ", ")),
			Fix: "packets arriving faster than the receive path could take them: lost inside the router, where no interface counter sees them. " +
				"One core taking all of an interface's interrupts is the common cause. Squeezes are not counted here — a squeeze is the kernel pacing itself, and it is normal.",
		})
	}
	return rep
}

func describeWindow(r Report) string {
	if r.Seconds <= 0 {
		return "in the agent's ring"
	}
	return fmt.Sprintf("in the last %.0f s", r.Seconds)
}

// portEvents counts, for one port, the kernel-log records the checks use.
type portEvents struct{ ownAddress, blocking, learning, forwarding, linkDown int }

// tally classifies every kernel-log record in the window by port and adds up
// the softnet drops per CPU.
func tally(board string, samples []sample.Sample) (perPort map[string]*portEvents, dropped uint64, dropCPUs map[int]bool) {
	perPort = map[string]*portEvents{}
	dropCPUs = map[int]bool{}
	// AnnotateKmsg does nothing at all for an empty board, and a board whose
	// device tree reports no model is ordinary. For a check that exists to
	// catch a loop, "no model" must not mean "no loop": any non-empty name
	// makes it classify the text and name the kernel port, and a board not in
	// the port table simply keeps the kernel's name for it.
	annotateAs := board
	if annotateAs == "" {
		annotateAs = "unknown"
	}
	for i := range samples {
		s := &samples[i]
		for _, raw := range s.Events {
			rec := raw
			procfs.AnnotateKmsg(annotateAs, &rec)
			if rec.Kind == "" {
				continue
			}
			port := rec.ROSIface
			if port == "" {
				port = rec.Iface // an unmapped board still names the kernel port
			}
			pe := perPort[port]
			if pe == nil {
				pe = &portEvents{}
				perPort[port] = pe
			}
			pe.count(rec.Kind)
		}
		for cpu, sn := range s.Softnet {
			if sn.Dropped > 0 {
				dropped += sn.Dropped
				dropCPUs[cpu] = true
			}
		}
	}
	return perPort, dropped, dropCPUs
}

func (pe *portEvents) count(kind string) {
	switch kind {
	case "own-address":
		pe.ownAddress++
	case "stp-blocking":
		pe.blocking++
	case "stp-learning":
		pe.learning++
	case "stp-forwarding":
		pe.forwarding++
	case "link-down":
		pe.linkDown++
	}
}

// portFindings turns one port's counts into what they show.
func portFindings(port string, pe *portEvents, window string) []Finding {
	var out []Finding
	switch {
	case pe.ownAddress >= ownAddressMin:
		detail := fmt.Sprintf("%d frames this router sent came back in on %s %s, carrying the bridge's own address as their source", pe.ownAddress, port, window)
		if pe.blocking > 0 {
			detail += fmt.Sprintf(", and STP blocked the port %d times", pe.blocking)
		}
		out = append(out, Finding{
			Check: "layer2-loop", Port: port, Detail: detail,
			Fix: "something downstream of " + port + " reaches the router by a second path. A mesh node with both a cable and a wireless backhaul is the usual cause, and so is a switch cabled twice. " +
				"While it lasts, STP keeps " + port + " blocked, so what is behind it reaches the router some other way or not at all — on the reference router it sent 1 packet a second for days, against 178 once the loop was gone. " +
				"RouterOS reports the port healthy: find the second path and break it.",
		})
	case pe.learning-pe.forwarding >= stpStuckMin:
		out = append(out, Finding{
			Check: "stp-churn", Port: port,
			Detail: fmt.Sprintf("STP moved %s to learning %d times %s and let it forward %d of them", port, pe.learning, window, pe.forwarding),
			Fix:    "the port is repeatedly being told it closes a loop, or the topology keeps changing behind it. Look for a second path to the router from whatever " + port + " connects to.",
		})
	}
	if pe.linkDown >= linkDownMin {
		out = append(out, Finding{
			Check: "link-flap", Port: port,
			Detail: fmt.Sprintf("%s lost link %d times %s", port, pe.linkDown, window),
			Fix:    "the cable, the connector, the device at the other end rebooting, or auto-negotiation failing. A port that flaps drops every connection through it each time.",
		})
	}
	return out
}
