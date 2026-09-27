//go:build linux

package lab

// The lab's file names and the arguments several commands share, named once.
const (
	baseDisk       = "base.qcow2"      // MikroTik's image, grown: the chain's root
	cleanDisk      = "clean.qcow2"     // the provisioned snapshot every reset returns to
	runDisk        = "run.qcow2"       // the live layer a reset replaces
	provisionDisk  = "provision.qcow2" // the layer provisioning writes before it becomes clean
	installDisk    = "install.qcow2"   // the RouterOS x86 ISO lab's target disk
	sumSuffix      = ".sha256"         // MikroTik's checksum file beside each download
	dockerLabel    = "--label"
	loopbackPrefix = "127.0.0.1:" // every lab port is published on the host's loopback only
	errLine        = "error: %v"
	noPubkeyAuth   = "PubkeyAuthentication=no"
	wanHost        = "lab-wan" // the ssh alias for the router's WAN side
)

// nothingToUnlock is what Lock returns when it took no lock.
func nothingToUnlock() {
	// Lock took nothing, so there is nothing to release.
}
