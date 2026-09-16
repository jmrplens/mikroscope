package sinks

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/derive"
)

// TestInfluxRendersBreadthSources covers the sources added on 2026-09-15 that
// cost no extra syscall — they were already read and thrown away: the fork
// counter, blocked tasks, the Err row of /proc/interrupts, the container's
// own cgroup events, /proc/buddyinfo and the MTD ECC state. The cgroup
// events must be absent, not zero, for a sample
// whose container had no cgroup2 to ask.
func TestInfluxRendersBreadthSources(t *testing.T) {
	s := &Influx{Host: "rb5009", Log: func(string) {}}
	s.Write(sqlRich(1))
	out := s.cur.String()
	for _, want := range []string{
		// 3 + 169*2 + 150*4 pages, the cross-check against nr_free_pages.
		"mikroscope_buddy,host=rb5009,node=0,zone=DMA free_pages=941u,order_0=3u,order_1=169u,order_2=150u ",
		"mikroscope_mtd,host=rb5009,device=mtd1,partition=RouterBoard\\ NAND\\ 1\\ Main corrected_bits=7u,ecc_failures=0u,bad_blocks=0u,bbt_blocks=0u,bitflip_threshold=12u,ecc_strength=16u ",
		// No ceilings published for the SPI part: no ceiling fields, not zeros.
		"mikroscope_mtd,host=rb5009,device=mtd2,partition=RouterBoot corrected_bits=0u,ecc_failures=0u,bad_blocks=0u,bbt_blocks=0u ",
		",throttled=2u,throttled_us=0u,oom_kill=1u,resets=0u,",
		"mikroscope_stat,host=rb5009 ctxt=0u,intr=0u,forks=0u,irq_total=0u,irq_err=0u ",
		",threads=150u,procs_blocked=0u ",
		",irq_total=0u,irq_err=0u ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("influx rendering lacks %q:\n%s", want, out)
		}
	}
	plain := &Influx{Host: "rb5009", Log: func(string) {}}
	plain.Write(kernel(1))
	if strings.Contains(plain.cur.String(), "throttled=") {
		t.Error("a sample without cgroup2 must not claim throttled=0")
	}
}

// TestTriggerMarkerReachesTheAnnotationSinks: the agent's capture marker is
// an annotation, so it lands where annotations are drawn from — one Influx
// row at the fire's wall clock, one SQL row, one Prometheus counter — and
// the file sink keeps the raw line.
func TestTriggerMarkerReachesTheAnnotationSinks(t *testing.T) {
	in := &Influx{Host: "rb5009", Log: func(string) {}}
	in.Write(trigger())
	want := `mikroscope_trigger,host=rb5009,cause=softnet-drop id=7u,seq=3u,value=1,threshold=0,field="softnet[0].dropped" 1788000000300000000` + "\n"
	if in.cur.String() != want {
		t.Errorf("influx marker:\n%s\nwant\n%s", in.cur.String(), want)
	}
	p, err := NewPrometheus(t.Context(), "127.0.0.1:0", 10, "t")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.Write(kernel(1))
	p.Write(trigger())
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+p.Addr()+"/metrics", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `mikroscope_collector_triggers_total{cause="softnet-drop"} 1`) {
		t.Errorf("prometheus lacks the trigger counter")
	}
}

// TestDeriveStageOutputReachesTheSinks: derived values ride beside the
// sample that produced them, the fast-path share beside the API poll, and a
// detection is a row of its own.
func TestDeriveStageOutputReachesTheSinks(t *testing.T) {
	in := &Influx{Host: "rb5009", Log: func(string) {}}
	k := kernel(1)
	k.Derived = derived(1)
	in.Write(k)
	a := api()
	a.Shares = shares()
	in.Write(a)
	in.Write(detection())
	out := in.cur.String()
	for _, want := range []string{
		"mikroscope_derived,host=rb5009 mem_pressure=2i,burst=true,suspect=false,cycles_per_packet=20.000,instructions_per_packet=5.000,cache_misses_per_packet=0.100,packets_per_irq=10.000 1788000000100000000\n",
		"mikroscope_derived_iface,host=rb5009,interface=ether1 rx_bytes=1000u,fp_rx_bytes=800u,tx_bytes=0u,fp_tx_bytes=0u,fp_rx_share=0.8000 ",
		"mikroscope_detection,host=rb5009,rule=microburst,key=cpu0 value=90,threshold=100,seq=3u,message=\"cpu0: 1 squeeze(s) and 0 drop(s) in a sample of 90 packets\" 1788000000300000000\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("influx lacks %q:\n%s", want, out)
		}
	}
	// Without a PMU the ratios are absent, not zero.
	plain := &Influx{Host: "rb5009", Log: func(string) {}}
	k2 := kernel(2)
	k2.Derived = &derive.Derived{Seq: 2}
	plain.Write(k2)
	if !strings.Contains(plain.cur.String(), "mikroscope_derived,host=rb5009 mem_pressure=0i,burst=false,suspect=false 1788000000200000000\n") {
		t.Errorf("absent ratios rendered as something:\n%s", plain.cur.String())
	}
}

// TestDeviceEventReachesTheSinks: the device-info stream is its own
// measurement — board facts at the collector's clock, one row per zone and
// core beside the identity — and the collector's Prometheus renders the
// same device families the agent does.
func TestDeviceEventReachesTheSinks(t *testing.T) {
	in := &Influx{Host: "rb5009", Log: func(string) {}}
	in.Write(device())
	out := in.cur.String()
	for _, want := range []string{
		"mikroscope_device,host=rb5009,board=RB5009,kernel=5.6.3 cores=4i,privileged=true,cgroup=true,sources=\"stat,thermal\",hash=\"0a1b2c3d\",conntrack_max=966656u,cgroup_mem_max=67108864u,ports_from=\"table\" ",
		"mikroscope_device_thermal,host=rb5009,zone=cpu-thermal critical_celsius=105.000,polling_ms=1000i ",
		"mikroscope_device_cpufreq,host=rb5009,cpu=0 cluster=0i,min_khz=350000u,max_khz=1400000u,governor=\"userspace\",steps=\"350000 1400000\" ",
		"mikroscope_device_cadence,host=rb5009,source=thermal,reason=declared hz=1 ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("influx lacks %q:\n%s", want, out)
		}
	}
	p, err := NewPrometheus(t.Context(), "127.0.0.1:0", 10, "t")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.Write(kernel(1))
	p.Write(device())
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+p.Addr()+"/metrics", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{
		`mikroscope_device_info{board="RB5009",kernel="5.6.3",cores="4",privileged="true",cgroup="true",ports_from="table",hash="0a1b2c3d"} 1`,
		`mikroscope_thermal_critical_celsius{zone="cpu-thermal"} 105`,
		`mikroscope_source_cadence_hz{source="thermal",reason="declared"} 1`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("collector prometheus lacks %q", want)
		}
	}
}
