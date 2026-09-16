package sinks

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jmrplens/mikroscope/internal/agent"
)

// The device-info stream (sinks.Event.Device): what the agent established
// about the board at start, handed to every sink once per capability hash.
// These are facts and settings, not samples — the board's identity, its
// declared ceilings, the cadence each level source is read at — so they are
// rendered as their own measurement, stamped with the collector's clock
// (they have none of their own), and never mixed into a sample row. This
// file holds the pieces the line-protocol, SQL and tree-shaped sinks share.

// deviceSources lists the enabled sources, sorted, as one comma-joined
// string field: what the agent could read on this deployment.
func deviceSources(c *agent.Capabilities) string {
	names := make([]string, 0, len(c.Sources))
	for n, on := range c.Sources {
		if on {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// deviceZones returns the thermal zones with a published ceiling or cadence,
// sorted by name.
func deviceZones(c *agent.Capabilities) []string {
	seen := map[string]bool{}
	for z := range c.Limits.ThermalCriticalMilliC {
		seen[z] = true
	}
	for z := range c.Limits.ThermalPollingMS {
		seen[z] = true
	}
	zones := make([]string, 0, len(seen))
	for z := range seen {
		zones = append(zones, z)
	}
	sort.Strings(zones)
	return zones
}

// deviceCores returns the cores with any cpufreq fact, sorted.
func deviceCores(c *agent.Capabilities) []int {
	seen := map[int]bool{}
	for k := range c.Limits.CPUFreqMinKHz {
		seen[k] = true
	}
	for k := range c.Limits.CPUFreqMaxKHz {
		seen[k] = true
	}
	for k := range c.Limits.CPUFreqGovernor {
		seen[k] = true
	}
	cores := make([]int, 0, len(seen))
	for k := range seen {
		cores = append(cores, k)
	}
	sort.Ints(cores)
	return cores
}

// deviceCluster is the lowest-numbered core that changes frequency with
// core, or core itself when nothing says otherwise.
func deviceCluster(c *agent.Capabilities, core int) int {
	lowest := core
	for _, r := range c.Limits.CPUFreqRelated[core] {
		lowest = min(lowest, r)
	}
	return lowest
}

// deviceSteps renders a core's DVFS ladder as a space-joined string.
func deviceSteps(c *agent.Capabilities, core int) string {
	steps := c.Limits.CPUFreqStepsKHz[core]
	parts := make([]string, 0, len(steps))
	for _, s := range steps {
		parts = append(parts, strconv.FormatUint(s, 10))
	}
	return strings.Join(parts, " ")
}

// writeDevice renders the device event in line protocol: one identity row,
// one row per thermal zone, one per core with cpufreq facts, one per
// source cadence.
func (s *Influx) writeDevice(c *agent.Capabilities, ts string) {
	fmt.Fprintf(&s.cur, "mikroscope_device%s,board=%s,kernel=%s cores=%di,privileged=%t,cgroup=%t,sources=%q,hash=%q", s.tag(), escapeTag(orUnknown(c.Board)), escapeTag(orUnknown(c.Kernel)), c.Cores, c.Privileged, c.Cgroup, deviceSources(c), c.Hash)
	if c.Limits.ConntrackMax > 0 {
		fmt.Fprintf(&s.cur, ",conntrack_max=%du", c.Limits.ConntrackMax)
	}
	if c.Limits.CgroupMemoryMaxBytes > 0 {
		fmt.Fprintf(&s.cur, ",cgroup_mem_max=%du", c.Limits.CgroupMemoryMaxBytes)
	}
	if c.PortsFrom != "" {
		fmt.Fprintf(&s.cur, ",ports_from=%q", c.PortsFrom)
	}
	fmt.Fprintf(&s.cur, " %s\n", ts)
	for _, z := range deviceZones(c) {
		fmt.Fprintf(&s.cur, "mikroscope_device_thermal%s,zone=%s", s.tag(), escapeTag(z))
		sep := " "
		if v := c.Limits.ThermalCriticalMilliC[z]; v > 0 {
			fmt.Fprintf(&s.cur, "%scritical_celsius=%s", sep, strconv.FormatFloat(float64(v)/1000, 'f', 3, 64))
			sep = ","
		}
		if v := c.Limits.ThermalPollingMS[z]; v > 0 {
			fmt.Fprintf(&s.cur, "%spolling_ms=%di", sep, v)
		}
		fmt.Fprintf(&s.cur, " %s\n", ts)
	}
	for _, core := range deviceCores(c) {
		fmt.Fprintf(&s.cur, "mikroscope_device_cpufreq%s,cpu=%d cluster=%di", s.tag(), core, deviceCluster(c, core))
		if v := c.Limits.CPUFreqMinKHz[core]; v > 0 {
			fmt.Fprintf(&s.cur, ",min_khz=%du", v)
		}
		if v := c.Limits.CPUFreqMaxKHz[core]; v > 0 {
			fmt.Fprintf(&s.cur, ",max_khz=%du", v)
		}
		if g := c.Limits.CPUFreqGovernor[core]; g != "" {
			fmt.Fprintf(&s.cur, ",governor=%q", g)
		}
		if st := deviceSteps(c, core); st != "" {
			fmt.Fprintf(&s.cur, ",steps=%q", st)
		}
		fmt.Fprintf(&s.cur, " %s\n", ts)
	}
	for _, name := range sortedStrings(c.Cadences) {
		fmt.Fprintf(&s.cur, "mikroscope_device_cadence%s,source=%s,reason=%s hz=%s %s\n", s.tag(), escapeTag(name), escapeTag(c.Cadences[name].Reason), strconv.FormatFloat(c.Cadences[name].Hz, 'g', -1, 64), ts)
	}
}

func orUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

// deviceRows renders the device event as SQL.
func (s *SQL) deviceRows(ts string, c *agent.Capabilities) {
	fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_device (time, host, board, kernel, cores, privileged, cgroup, sources, conntrack_max, cgroup_mem_max, ports_from, hash) VALUES (%s, %s, %s, %s, %d, %t, %t, %s, %s, %s, %s, %s)%s",
		ts, s.hostLit, sqlLabel(c.Board), sqlLabel(c.Kernel), c.Cores, c.Privileged, c.Cgroup, sqlQuote(deviceSources(c)), sqlNullIfZero(c.Limits.ConntrackMax), sqlNullIfZero(c.Limits.CgroupMemoryMaxBytes), sqlLabel(c.PortsFrom), sqlQuote(c.Hash), sqlEnd)
	for _, z := range deviceZones(c) {
		crit, poll := "NULL", "NULL"
		if v := c.Limits.ThermalCriticalMilliC[z]; v > 0 {
			crit = sqlFloat(float64(v)/1000, 3)
		}
		if v := c.Limits.ThermalPollingMS[z]; v > 0 {
			poll = strconv.Itoa(v)
		}
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_device_thermal (time, host, zone, critical_celsius, polling_ms) VALUES (%s, %s, %s, %s, %s)%s", ts, s.hostLit, sqlQuote(z), crit, poll, sqlEnd)
	}
	for _, core := range deviceCores(c) {
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_device_cpufreq (time, host, cpu, cluster, min_khz, max_khz, governor, steps) VALUES (%s, %s, %d, %d, %s, %s, %s, %s)%s",
			ts, s.hostLit, core, deviceCluster(c, core), sqlNullIfZero(c.Limits.CPUFreqMinKHz[core]), sqlNullIfZero(c.Limits.CPUFreqMaxKHz[core]), sqlLabel(c.Limits.CPUFreqGovernor[core]), sqlLabel(deviceSteps(c, core)), sqlEnd)
	}
	for _, name := range sortedStrings(c.Cadences) {
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_device_cadence (time, host, source, reason, hz) VALUES (%s, %s, %s, %s, %s)%s", ts, s.hostLit, sqlQuote(name), sqlQuote(c.Cadences[name].Reason), sqlFloat(c.Cadences[name].Hz, -1), sqlEnd)
	}
}
