package router

import "testing"

// Installed is what `upgrade` asks before it replaces anything: it is one
// connect, and it is true only when EVERY step of the plan is owned. A
// partially installed device has to read as not installed, or upgrade would
// replace a container whose veth and firewall objects are somebody else's.
func TestInstalledIsAllStepsOrNone(t *testing.T) {
	t.Parallel()
	o := Defaults()
	if err := o.Finish(); err != nil {
		t.Fatal(err)
	}

	none := &fakeRunner{present: map[string]bool{}}
	got, err := Installed(none, o)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("a device with none of the objects reported as installed")
	}

	all := map[string]bool{o.Veth: true, o.Name: true, o.IfaceList: true, o.AddrList: true, o.Subnet: true, o.ContainerIP: true}
	got, err = Installed(&fakeRunner{present: all, owned: all}, o)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("a device with every object owned reported as not installed")
	}

	// A device carrying only one of the objects is not installed, whichever
	// one it is. Stated this way round rather than "all but one missing",
	// because the fake matches commands by substring and several identifiers
	// appear in the same command — dropping one key does not reliably take a
	// step's count to zero, while keeping only one does.
	for only := range all {
		// o.Name is "mikroscope", which is a substring of the ownership tag
		// every command carries, so the fake answers 1 to all of them and the
		// case says nothing about Installed.
		if only == o.Name {
			continue
		}
		got, err = Installed(&fakeRunner{present: map[string]bool{only: true}, owned: map[string]bool{only: true}}, o)
		if err != nil {
			t.Fatal(err)
		}
		if got {
			t.Errorf("a device carrying only %q reported as installed", only)
		}
	}
}
