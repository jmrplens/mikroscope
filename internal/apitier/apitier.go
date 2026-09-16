// Package apitier samples what the container cannot see, over one persistent
// RouterOS API connection at 1 Hz: per-core load as RouterOS reports it,
// memory, health, interface rates, and, opt-in at its own cadence, the
// conntrack count. 1 Hz is RouterOS's own internal update cadence — on the
// reference RB5009 cpu-load is refreshed once per second, so sampling it at
// 100 ms returns runs of about ten identical values and buys nothing.
//
// No command here calls `/tool profile`: the `test` policy the RouterOS user
// needs is for the relay transport's `/tool fetch` alone, so `read,api` is
// enough for every API-tier mode.
package apitier

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	rosapi "github.com/jmrplens/mikroscope/internal/rosapi"
)

// Client is the slice of the API client the tier uses; tests fake it.
type Client interface {
	// RunArgsContext runs one sentence and returns its reply.
	RunArgsContext(ctx context.Context, sentences []string) (*rosapi.Reply, error)
}

// Sample is one 1 Hz read of the API tier. Field presence follows what the
// device answered; a failed command leaves its part nil.
type Sample struct {
	WallNS int64              `json:"wall_ns"` // agent clock (host clock + skew)
	System *System            `json:"system,omitempty"`
	Cores  []Core             `json:"cores,omitempty"`
	Health map[string]float64 `json:"health,omitempty"`
	Ifaces []Iface            `json:"ifaces,omitempty"`
	// IfaceCounters is the router's own per-port counter set, on its own slow
	// cadence (Options.CountersEvery). Present only on the rounds that polled
	// it. See IfaceCounters for why it exists beside Ifaces.
	IfaceCounters []IfaceCounters `json:"iface_counters,omitempty"`
	Conntrack     *uint64         `json:"conntrack,omitempty"`
	// Inventory is every interface's configuration as the router holds it —
	// type, role, bridge, comment — present only on the rounds that read it:
	// the first round and then every Options.LabelsEvery. See IfaceInfo.
	Inventory []IfaceInfo `json:"inventory,omitempty"`
	Errors    []string    `json:"errors,omitempty"`
}

// IfaceInfo is what one interface IS, as opposed to what it carries: the
// configuration that decides how its numbers may be read, from three reads
// that return configuration only (`/interface/print` restricted to
// name, default-name, type, comment and actual-mtu; `/interface/list/member`;
// `/interface/bridge/port`).
//
// It exists because the counters alone do not say which of them can be
// compared. On the reference RB5009 (RouterOS 7.24.2, 2026-09-16) `bridge` is
// the bridge's CPU-side port and `ether1` is a wire whose traffic the switch
// chip mostly forwards without the CPU: ether1 received 255.8 GB on the wire
// and 29.7 GB of it reached the CPU. Drawn side by side without their type
// they read as peers, and summed they double-count. The type and the bridge
// membership are what a panel needs to keep them apart.
//
// It is configuration, so it is read once at start and then on the slow
// labels cadence, never per poll: the API reads it costs are three short
// replies every five minutes.
type IfaceInfo struct {
	Name string `json:"name"`
	// DefaultName is the factory name of a physical port ("ether5") when the
	// operator renamed it, and equal to Name otherwise. The kernel log names
	// ports by the board's default names (procfs.RouterOSName), so this is
	// what lines a kernel record up with the current name. Empty for an
	// interface that has no default name (a bridge, a VLAN, a tunnel).
	DefaultName string `json:"default_name,omitempty"`
	// Type is RouterOS's own: ether, bridge, vlan, pppoe-out, wg, veth, ….
	// An ether that is a bridge member counts wire traffic, including frames
	// the switch chip forwarded in hardware; a bridge counts its CPU side.
	Type    string `json:"type,omitempty"`
	Comment string `json:"comment,omitempty"`
	// Role is the interface lists the interface belongs to, sorted and
	// comma-joined ("WAN", "LAN,VPN"). A bridge member that is in no list of
	// its own takes its bridge's lists, because that is how RouterOS
	// firewall rules match it. Empty when no list names it.
	Role string `json:"role,omitempty"`
	// Bridge is the bridge the interface is a port of, empty when none.
	Bridge string `json:"bridge,omitempty"`
	MTU    uint64 `json:"mtu,omitempty"` // actual-mtu; 0 when not reported
}

// System is /system/resource: RouterOS's 1 s cpu-load and memory view.
type System struct {
	CPULoad     uint64 `json:"cpu_load"`
	FreeMemory  uint64 `json:"free_memory"`
	TotalMemory uint64 `json:"total_memory"`
	FreeHDD     uint64 `json:"free_hdd"`
	UptimeS     uint64 `json:"uptime_s"`
	Version     string `json:"version"`
}

// Core is one row of /system/resource/cpu: percentages, as RouterOS sees
// them — a cross-check against the kernel tier, not a replacement.
type Core struct {
	Load, IRQ, Disk uint64
}

// Iface is one interface's instantaneous rates from monitor-traffic.
//
// The four traffic rates are always returned. The loss rates are not: on the
// reference RB5009 (RouterOS 7.24.2, 2026-09-15) monitor-traffic returns
// rx-drops, tx-drops and tx-queue-drops per second and NO error keys at all,
// and until that day this struct carried RxErrors/TxErrors fields that read a
// fabricated 0 for every port — a panel that said "no errors" on a port with
// 650 526 rx-overflow events. Losses holds exactly the loss keys the router
// returned, keyed by RouterOS's own
// name with the "-per-second" suffix dropped ("rx-drops", "tx-queue-drops");
// a key the router did not return is absent, never 0.
type Iface struct {
	Name                       string `json:"name"`
	RxBps, TxBps, RxPps, TxPps uint64
	Losses                     map[string]uint64 `json:"losses,omitempty"`
	// Comment is the interface's RouterOS comment ("TrueNAS - High Performance
	// Storage"), the human label an operator gave the port. It turns a
	// per-interface panel from "ether1" into what is actually plugged in. It
	// is CONFIG, not telemetry: read with the inventory and carried on every sample, so
	// the sinks can tag the row without a second lookup. Empty when the port
	// has no comment or comments were not fetched.
	Comment string `json:"comment,omitempty"`
	// Type, Role and Bridge are the interface's IfaceInfo fields, carried on
	// the row for the same reason as Comment. Empty until the inventory has
	// been read, and for an interface it does not list.
	Type   string `json:"type,omitempty"`
	Role   string `json:"role,omitempty"`
	Bridge string `json:"bridge,omitempty"`
}

// IfaceCounters is one interface's cumulative counters as RouterOS keeps
// them, keyed by RouterOS's own field name ("rx-overflow", "fp-rx-byte",
// "link-downs"). Two commands feed it, merged per interface:
// `/interface/ethernet print stats` (the MAC's typed error taxonomy, the
// collision family, the frame-size buckets, the driver counters) and
// `/interface print stats-detail` (the fast-path fp-* counters, link-downs,
// tx-queue-drop, the kernel-side byte/packet totals).
//
// WHY A MAP AND NOT A STRUCT. On 2026-09-15 the reference router's ether1 had
// 652 364 rx-overflow events, growing, and mikroscope reported "no errors" on
// it — because Iface.RxErrors is read from monitor-traffic, which does not
// return an error key for that port at all, and num() turns an absent key
// into a measured 0. A struct of 45 uint64 fields would repeat that shape for
// every counter this board or the next one happens not to report. A map holds
// exactly the keys the router returned: absent stays absent, and a board that
// has no collision counters (an SFP cage) produces no collision entries rather
// than a row of zeroes that reads as "no collisions".
//
// Only numeric fields that count something are kept (numericField). Names,
// MACs, timestamps and sizes such as mtu are dropped; what the interface is
// travels in the fields below, from the inventory.
//
// These are CUMULATIVE COUNTERS since boot or since the port's last reset,
// unlike Ifaces' instantaneous rates, so a consumer differences them — and
// they are polled slowly because a counter's information is in its change,
// not in reading it forty times a second. The single most useful derivation
// they enable, measured on ether1 (2.5 GbE to a NAS): rx-bytes 211.8 GB on
// the wire, driver-rx-byte 27.0 GB delivered to the CPU, fp-rx-byte 27.0 GB of
// that through the fast path — i.e. 87 % of that port's traffic was switched
// in hardware and never touched the CPU, and nearly all of the rest bypassed
// the slow path. No other source in this project can say that.
type IfaceCounters struct {
	Name     string            `json:"name"`
	Counters map[string]uint64 `json:"counters"`
	Comment  string            `json:"comment,omitempty"`
	Type     string            `json:"type,omitempty"`
	Role     string            `json:"role,omitempty"`
	Bridge   string            `json:"bridge,omitempty"`
}

// Options configure the tier.
type Options struct {
	Interfaces     []string      // for monitor-traffic; empty = none
	ConntrackEvery time.Duration // 0 = never
	LabelsEvery    time.Duration // inventory (labels, type, role, bridge) re-read cadence; 0 = default 5 min
	CountersEvery  time.Duration // per-port cumulative counters cadence; 0 = never
	Health         bool
}

// Reader runs the commands. Skew is added to the host clock so samples are
// stamped in the agent's time.
type Reader struct {
	Client        Client
	Opts          Options
	SkewNS        int64
	lastConntrack time.Time
	// inv maps interface name to its configuration. It is re-read on a slow
	// cadence (LabelsEvery, default 5 min) rather than every poll — but it IS
	// re-read, because an operator can edit a comment or move a port between
	// lists and the dashboard should follow within minutes, not only at the
	// next collector restart. nil until the first successful read.
	inv          map[string]IfaceInfo
	byDefault    map[string]string // default name -> current name
	lastInv      time.Time
	triedInv     bool // set once the first read has been attempted
	invUnsent    bool // a successful read not yet carried on a Sample
	lastCounters time.Time
}

// Read performs one round. It never fails as a whole: each command's error
// is recorded in Sample.Errors and the rest is still returned.
func (r *Reader) Read(ctx context.Context) Sample {
	s := Sample{WallNS: time.Now().UnixNano() + r.SkewNS}
	r.inventory(ctx, &s)
	if sys, err := r.system(ctx); err != nil {
		s.Errors = append(s.Errors, "resource: "+err.Error())
	} else {
		s.System = sys
	}
	if cores, err := r.cores(ctx); err != nil {
		s.Errors = append(s.Errors, "resource/cpu: "+err.Error())
	} else {
		s.Cores = cores
	}
	if r.Opts.Health {
		if h, err := r.health(ctx); err != nil {
			s.Errors = append(s.Errors, "health: "+err.Error())
		} else {
			s.Health = h
		}
	}
	if len(r.Opts.Interfaces) > 0 {
		if ifs, err := r.ifaces(ctx); err != nil {
			s.Errors = append(s.Errors, "monitor-traffic: "+err.Error())
		} else {
			s.Ifaces = ifs
		}
	}
	if r.Opts.CountersEvery > 0 && time.Since(r.lastCounters) >= r.Opts.CountersEvery {
		r.lastCounters = time.Now()
		if c, err := r.counters(ctx); err != nil {
			s.Errors = append(s.Errors, "counters: "+err.Error())
		} else {
			s.IfaceCounters = c
		}
	}
	if r.Opts.ConntrackEvery > 0 && time.Since(r.lastConntrack) >= r.Opts.ConntrackEvery {
		r.lastConntrack = time.Now()
		if n, err := r.conntrack(ctx); err != nil {
			s.Errors = append(s.Errors, "conntrack: "+err.Error())
		} else {
			s.Conntrack = &n
		}
	}
	return s
}

// inventory re-reads the configuration when it is due and hands it on once
// per successful read — including one the forwarder made before the first
// round, so the sinks hold it from the start.
func (r *Reader) inventory(ctx context.Context, s *Sample) {
	if r.inventoryDue() {
		if err := r.LoadInventory(ctx); err != nil {
			s.Errors = append(s.Errors, "inventory: "+err.Error())
		}
	}
	if r.invUnsent {
		r.invUnsent = false
		s.Inventory = r.InventoryList()
	}
}

func (r *Reader) run(ctx context.Context, words ...string) (*rosapi.Reply, error) {
	reply, err := r.Client.RunArgsContext(ctx, words)
	if err != nil {
		return nil, err
	}
	if reply == nil || len(reply.Re) == 0 {
		return nil, errors.New("empty reply")
	}
	return reply, nil
}

// LossKeys are the monitor-traffic per-second loss keys a router MAY return,
// in the order the sinks render them. Read through numPresent, never num:
// which of them a RouterOS version returns is the router's statement to make.
var LossKeys = [...]string{"rx-drops", "tx-drops", "tx-queue-drops", "rx-errors", "tx-errors"}

// num reads a key the command ALWAYS returns; an absent key reads 0. It is
// used only for /system/resource, /system/resource/cpu and the four
// monitor-traffic traffic rates, each of which every RouterOS version returns
// on every row. Any key a router may omit goes through numPresent instead:
// reporting "no errors" on a port with 650 526 rx-overflow events was
// exactly a num() on such a key.
func num(m map[string]string, key string) uint64 {
	v := strings.TrimSpace(m[key])
	v = strings.TrimSuffix(v, "%")
	n, _ := strconv.ParseUint(v, 10, 64)
	return n
}

// numPresent reads a key that may be absent, and says so.
func numPresent(m map[string]string, key string) (uint64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), "%"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func (r *Reader) system(ctx context.Context) (*System, error) {
	reply, err := r.run(ctx, "/system/resource/print", "=.proplist=cpu-load,free-memory,total-memory,free-hdd-space,uptime,version")
	if err != nil {
		return nil, err
	}
	m := reply.Re[0].Map
	return &System{
		CPULoad: num(m, "cpu-load"), FreeMemory: num(m, "free-memory"), TotalMemory: num(m, "total-memory"),
		FreeHDD: num(m, "free-hdd-space"), UptimeS: parseUptime(m["uptime"]), Version: m["version"],
	}, nil
}

func (r *Reader) cores(ctx context.Context) ([]Core, error) {
	reply, err := r.run(ctx, "/system/resource/cpu/print", "=.proplist=cpu,load,irq,disk")
	if err != nil {
		return nil, err
	}
	out := make([]Core, 0, len(reply.Re))
	for _, sen := range reply.Re {
		out = append(out, Core{Load: num(sen.Map, "load"), IRQ: num(sen.Map, "irq"), Disk: num(sen.Map, "disk")})
	}
	return out, nil
}

func (r *Reader) health(ctx context.Context) (map[string]float64, error) {
	reply, err := r.run(ctx, "/system/health/print", "=.proplist=name,value")
	if err != nil {
		return nil, err
	}
	out := map[string]float64{}
	for _, sen := range reply.Re {
		if v, convErr := strconv.ParseFloat(strings.TrimSpace(sen.Map["value"]), 64); convErr == nil && sen.Map["name"] != "" {
			out[sen.Map["name"]] = v
		}
	}
	return out, nil
}

func (r *Reader) ifaces(ctx context.Context) ([]Iface, error) {
	reply, err := r.run(ctx, "/interface/monitor-traffic", "=interface="+strings.Join(r.Opts.Interfaces, ","), "=once=")
	if err != nil {
		return nil, err
	}
	out := make([]Iface, 0, len(reply.Re))
	for _, sen := range reply.Re {
		m := sen.Map
		f := Iface{
			Name: m["name"], RxBps: num(m, "rx-bits-per-second"), TxBps: num(m, "tx-bits-per-second"),
			RxPps: num(m, "rx-packets-per-second"), TxPps: num(m, "tx-packets-per-second"),
		}
		info := r.inv[f.Name]
		f.Comment, f.Type, f.Role, f.Bridge = info.Comment, info.Type, info.Role, info.Bridge
		for _, k := range LossKeys {
			if v, ok := numPresent(m, k+"-per-second"); ok {
				if f.Losses == nil {
					f.Losses = make(map[string]uint64, len(LossKeys))
				}
				f.Losses[k] = v
			}
		}
		out = append(out, f)
	}
	return out, nil
}

func (r *Reader) inventoryDue() bool {
	every := r.Opts.LabelsEvery
	if every <= 0 {
		every = 5 * time.Minute
	}
	return !r.triedInv || time.Since(r.lastInv) >= every
}

// LoadInventory reads what every interface is: name, default name, type,
// comment and MTU from `/interface`, the interface-list memberships, and the
// bridge ports. The forwarder calls it once before the first kernel pull, so
// kernel-log records are labeled from the start; Read then repeats it every
// LabelsEvery.
//
// It reads `/interface` and not `/interface/ethernet` on purpose: a bridge, a
// VLAN or a PPPoE link has a type and a comment too. Every read names its
// properties, and none of them can carry a secret, so what it holds is
// configuration and nothing else.
//
// A failed `/interface` read keeps the inventory already held rather than
// dropping it — a transient API error should not blank every panel's label —
// and is returned. The list and bridge reads are best effort: without them
// the inventory still has names, types and comments.
func (r *Reader) LoadInventory(ctx context.Context) error {
	r.triedInv = true
	r.lastInv = time.Now()
	reply, err := r.Client.RunArgsContext(ctx, []string{"/interface/print", "=.proplist=name,default-name,type,comment,actual-mtu"})
	if err != nil {
		return err
	}
	if reply == nil {
		return errors.New("empty reply")
	}
	inv, byDefault := parseInterfaces(reply)
	r.readBridgePorts(ctx, inv)
	lists := r.readListMembers(ctx)
	for name, info := range inv {
		roles := lists[name]
		if len(roles) == 0 && info.Bridge != "" {
			roles = lists[info.Bridge]
		}
		info.Role = joinSorted(roles)
		inv[name] = info
	}
	// Replace wholesale: a comment REMOVED in RouterOS must disappear here
	// too, which a merge would not do.
	r.inv, r.byDefault = inv, byDefault
	r.invUnsent = true
	return nil
}

// parseInterfaces turns the /interface reply into the inventory's own shape,
// with the map from default name to current name beside it.
func parseInterfaces(reply *rosapi.Reply) (inv map[string]IfaceInfo, byDefault map[string]string) {
	inv = map[string]IfaceInfo{}
	byDefault = map[string]string{}
	for _, sen := range reply.Re {
		name := sen.Map["name"]
		if name == "" {
			continue
		}
		info := IfaceInfo{Name: name, DefaultName: sen.Map["default-name"], Type: sen.Map["type"], Comment: strings.TrimSpace(sen.Map["comment"])}
		info.MTU, _ = numPresent(sen.Map, "actual-mtu")
		if info.DefaultName != "" {
			byDefault[info.DefaultName] = name
		}
		inv[name] = info
	}
	return inv, byDefault
}

// readListMembers maps interface name to the interface lists that name it.
// Best effort: without it the inventory still has names, types and comments.
func (r *Reader) readListMembers(ctx context.Context) map[string][]string {
	lists := map[string][]string{}
	lr, err := r.Client.RunArgsContext(ctx, []string{"/interface/list/member/print", "=.proplist=list,interface"})
	if err != nil || lr == nil {
		return lists
	}
	for _, sen := range lr.Re {
		if i, l := sen.Map["interface"], sen.Map["list"]; i != "" && l != "" {
			lists[i] = append(lists[i], l)
		}
	}
	return lists
}

// readBridgePorts fills in each interface's bridge. A bridge port can name an
// interface LIST instead of an interface; that is not a port to label.
func (r *Reader) readBridgePorts(ctx context.Context, inv map[string]IfaceInfo) {
	br, err := r.Client.RunArgsContext(ctx, []string{"/interface/bridge/port/print", "=.proplist=interface,bridge"})
	if err != nil || br == nil {
		return
	}
	for _, sen := range br.Re {
		if info, ok := inv[sen.Map["interface"]]; ok {
			info.Bridge = sen.Map["bridge"]
			inv[info.Name] = info
		}
	}
}

func joinSorted(v []string) string {
	if len(v) == 0 {
		return ""
	}
	c := slices.Clone(v)
	slices.Sort(c)
	return strings.Join(slices.Compact(c), ",")
}

// InventoryList returns the inventory sorted by name; nil before the first
// successful read.
func (r *Reader) InventoryList() []IfaceInfo {
	if r.inv == nil {
		return nil
	}
	out := make([]IfaceInfo, 0, len(r.inv))
	for _, info := range r.inv {
		out = append(out, info)
	}
	slices.SortFunc(out, func(a, b IfaceInfo) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Port looks an interface up by the board's default name, which is how the
// kernel log names it (procfs.RouterOSName), and returns its current
// configuration. False before the first read, or when no interface has that
// default name.
func (r *Reader) Port(defaultName string) (IfaceInfo, bool) {
	name, ok := r.byDefault[defaultName]
	if !ok {
		return IfaceInfo{}, false
	}
	info, ok := r.inv[name]
	return info, ok
}

// counters runs the two per-port counter commands and merges them by
// interface name. Every interface the router lists is included — not only
// Options.Interfaces — because the counters are the inventory the rate view
// deliberately narrows: the port a fault lives on is often one nobody thought
// to monitor (the layer-2 loop of 2026-09-12 was on a port absent from the
// monitor-traffic list). Both replies are one round trip each.
func (r *Reader) counters(ctx context.Context) ([]IfaceCounters, error) {
	byName := map[string]map[string]uint64{}
	var order []string
	merge := func(reply *rosapi.Reply) {
		for _, sen := range reply.Re {
			name := sen.Map["name"]
			if name == "" {
				continue
			}
			m, ok := byName[name]
			if !ok {
				m = map[string]uint64{}
				byName[name] = m
				order = append(order, name)
			}
			for k, v := range sen.Map {
				if n, isNum := numericField(k, v); isNum {
					m[k] = n
				}
			}
		}
	}
	eth, err := r.run(ctx, "/interface/ethernet/print", "=stats=")
	if err != nil {
		return nil, fmt.Errorf("ethernet stats: %w", err)
	}
	merge(eth)
	all, err := r.run(ctx, "/interface/print", "=stats-detail=")
	if err != nil {
		return nil, fmt.Errorf("stats-detail: %w", err)
	}
	merge(all)
	out := make([]IfaceCounters, 0, len(order))
	for _, name := range order {
		info := r.inv[name]
		out = append(out, IfaceCounters{Name: name, Counters: byName[name], Comment: info.Comment, Type: info.Type, Role: info.Role, Bridge: info.Bridge})
	}
	return out, nil
}

// notCounters are the plain integers the two counter commands return that
// count nothing: sizes and a configured temperature. They parse as numbers,
// and every sink renders this map as a counter family (a Prometheus
// `_total`, an increase() over a bin), so they are dropped here rather than
// left for each consumer to know about. The MTU that matters travels as
// IfaceInfo.MTU.
var notCounters = map[string]bool{
	"mtu": true, "actual-mtu": true, "l2mtu": true, "max-l2mtu": true,
	"sfp-shutdown-temperature": true,
}

// numericField keeps a RouterOS field only when it is a plain unsigned
// integer counter. ".id", names, MACs, "true"/"false", timestamps and
// "unlimited/unlimited" are all rejected by the parse, and the sizes in
// notCounters by name; any other value that parses is a count.
func numericField(key, value string) (uint64, bool) {
	if key == "" || key[0] == '.' || notCounters[key] {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func (r *Reader) conntrack(ctx context.Context) (uint64, error) {
	reply, err := r.Client.RunArgsContext(ctx, []string{"/ip/firewall/connection/print", "=count-only="})
	if err != nil {
		return 0, err
	}
	if reply != nil && reply.Done != nil {
		if v, ok := reply.Done.Map["ret"]; ok {
			return strconv.ParseUint(v, 10, 64)
		}
	}
	for _, sen := range reply.Re {
		if v, ok := sen.Map["ret"]; ok {
			return strconv.ParseUint(v, 10, 64)
		}
	}
	return 0, errors.New("count-only returned no ret")
}

// parseUptime turns RouterOS "1w2d3h4m5s" into seconds.
func parseUptime(s string) uint64 {
	var total, n uint64
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
			n = n*10 + uint64(c-'0')
		case c == 'w':
			total, n = total+n*7*86400, 0
		case c == 'd':
			total, n = total+n*86400, 0
		case c == 'h':
			total, n = total+n*3600, 0
		case c == 'm':
			total, n = total+n*60, 0
		case c == 's':
			total, n = total+n, 0
		default:
			n = 0
		}
	}
	return total
}

// Dial opens the persistent connection with the command timeout the tier
// needs: one command in flight at a time, bounded.
func Dial(ctx context.Context, addr, user, password string) (*rosapi.Client, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, err := rosapi.DialContext(dialCtx, addr, user, password)
	if err != nil {
		return nil, fmt.Errorf("api %s: %w", addr, err)
	}
	c.SetCommandTimeout(5 * time.Second)
	return c, nil
}
