package procfs

import (
	"bytes"
	"fmt"
)

// IRQ is one row of /proc/interrupts. ID is the number or the name (IPI0,
// Err); PerCPU has one count per CPU column for numbered and IPI rows, and
// a single value for rows like Err/MIS that carry no per-CPU columns.
// Description is the rest of the line, trimmed — chip, hwirq, trigger and
// the device names. On the reference RB5009 (RouterOS 7.24.2, kernel 5.6.3
// arm64, 2026-09-11) `switch0` is four IRQs (35–38), each pinned to one core,
// carrying 92–103 M counts apiece.
type IRQ struct {
	ID          string
	PerCPU      []uint64
	Description string
}

// Total sums PerCPU.
func (i IRQ) Total() uint64 {
	var t uint64
	for _, v := range i.PerCPU {
		t += v
	}
	return t
}

// ParseInterrupts parses /proc/interrupts.
func ParseInterrupts(b []byte) ([]IRQ, error) {
	var out []IRQ
	cpus := -1
	var err error
	lines(b, func(line []byte) bool {
		if cpus < 0 {
			cpus = countFields(line)
			if cpus == 0 {
				err = fmt.Errorf("%w: interrupts header", ErrFormat)
				return false
			}
			return true
		}
		if len(bytes.TrimSpace(line)) == 0 {
			return true
		}
		id, rest := nextField(line)
		if len(id) < 2 || id[len(id)-1] != ':' {
			err = fmt.Errorf("%w: interrupts row %q", ErrFormat, id)
			return false
		}
		irq := IRQ{ID: string(id[:len(id)-1])}
		for range cpus {
			f, after := nextField(rest)
			v, ok := parseUint(f)
			if !ok {
				break // description starts here (or the row has fewer columns)
			}
			irq.PerCPU = append(irq.PerCPU, v)
			rest = after
		}
		if len(irq.PerCPU) == 0 {
			err = fmt.Errorf("%w: interrupts row %s without counts", ErrFormat, irq.ID)
			return false
		}
		irq.Description = string(bytes.TrimSpace(rest))
		out = append(out, irq)
		return true
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: interrupts without rows", ErrFormat)
	}
	return out, nil
}
