//go:build linux

package lab

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// DefaultRegistryURL is the registry-url the lab gives with a credential:
// Docker Hub's registry host, which the lab's pull scenarios pull from
// (LAB_REMOTE_IMAGE and the golden scripts), written as mikroscope writes it
// into every remote-image= (registry-1.docker.io/jmrplens/…), with no
// scheme. RouterOS presents /container/config's username and password for a
// reference whose host is registry-url as written, and for a reference with
// no host, which it pulls from registry-url. Measured in the lab (CHR 7.24.4
// x86_64, 2026-09-27) with a deliberately wrong credential, which Docker
// Hub's token endpoint answers with 401: registry-1.docker.io/jmrplens/
// mikroscope-agent:1.3.1 failed with `auth error` under
// registry-url=registry-1.docker.io, and was pulled, anonymously, under
// https://registry-1.docker.io and https://registry-1.docker.io/; the
// reference without a host failed with `auth error` under
// https://registry-1.docker.io. So the URL MikroTik's examples use would
// leave every pull of the suite anonymous.
const DefaultRegistryURL = "registry-1.docker.io"

// Registry is the credential the lab router pulls container images with:
// LAB_REGISTRY_URL, LAB_REGISTRY_USER and LAB_REGISTRY_TOKEN, read from the
// environment and never from a file of the lab's.
//
// Docker Hub counts anonymous pulls per address, and a CI runner's address
// is shared with whatever else ran from it, so a suite that pulls a dozen
// times can find the allowance spent by others. With a user and a token the
// router's pulls count against that account instead. The lab only pulls, so
// a read-only token is enough; CI gives it the repository's DOCKERHUB_TOKEN
// (lab.yml says why).
//
// Without them the router pulls anonymously, as RouterOS ships, and the lab
// does nothing: /container/config stays as the snapshot has it.
type Registry struct {
	URL   string // LAB_REGISTRY_URL, registry-url: DefaultRegistryURL unless set
	User  string // LAB_REGISTRY_USER
	Token string // LAB_REGISTRY_TOKEN: never printed, never an argument
}

// Set says whether the router gets a credential. Load refuses a user
// without a token and a token without a user, so the two come together.
func (r Registry) Set() bool { return r.User != "" && r.Token != "" }

// String names the credential's variables, never their values, so that a
// Config printed whole shows neither.
func (r Registry) String() string {
	if !r.Set() {
		return "anonymous"
	}
	return r.URL + " as LAB_REGISTRY_USER with LAB_REGISTRY_TOKEN"
}

// GoString is String, for %#v.
func (r Registry) GoString() string { return r.String() }

// registryURL is a registry as /container/config's registry-url takes it: a
// host, perhaps with a scheme, a port and a path, and no user or password
// in it.
var registryURL = regexp.MustCompile(`^(https?://)?[A-Za-z0-9.-]+(:\d{1,5})?(/[A-Za-z0-9._~/-]*)?$`)

// validate refuses a credential the router could not be given as it is: one
// half of it, a URL that is not a registry's, and a user or a token with a
// space, a control character or a byte beyond ASCII in it. The messages
// name the variable, never its value, the URL included: a URL refused for
// an @ in it may carry a password.
func (r Registry) validate() error {
	if (r.User == "") != (r.Token == "") {
		return errors.New("LAB_REGISTRY_USER and LAB_REGISTRY_TOKEN go together: set both for authenticated pulls, or neither for anonymous ones")
	}
	if !registryURL.MatchString(r.URL) {
		return fmt.Errorf("LAB_REGISTRY_URL is not a registry (a host, perhaps with a scheme, a port and a path; no user or password), such as %s", DefaultRegistryURL)
	}
	for _, v := range []struct{ name, value string }{{"LAB_REGISTRY_USER", r.User}, {"LAB_REGISTRY_TOKEN", r.Token}} {
		for i := range len(v.value) {
			if c := v.value[i]; c <= ' ' || c > '~' {
				return fmt.Errorf("%s holds a space, a control character or a byte beyond ASCII: the lab passes printable ASCII only", v.name)
			}
		}
	}
	return nil
}

// RegistryScript is the RouterOS script that gives /container/config the
// credential: registry-url, username and password in one command. It is
// written to a file and imported, never passed as an argument, and every
// value in it is escaped whole (RouterOSEscaped), since a token may hold any
// printable character.
func RegistryScript(r Registry) string {
	return "/container/config/set registry-url=" + RouterOSEscaped(r.URL) +
		" username=" + RouterOSEscaped(r.User) + " password=" + RouterOSEscaped(r.Token) + "\n"
}

// RouterOSEscaped is s as a RouterOS string literal in which every byte that
// is not an ASCII letter or digit is written as \HH, its value in two
// upper-case hexadecimal digits. MikroTik's Scripting manual lists the
// escape (`\xx`, "print character from hex value. Hex numbers should use
// capital letters"); the lettered escapes (\a, \b, \f, \n, \r, \t, \v, \_)
// are lower case or not a hexadecimal digit, so none of these can be read as
// one of them. Measured in the lab (CHR 7.24.4 x86_64, 2026-09-27): a
// username made of all 32 printable ASCII characters that are neither a
// letter nor a digit, between an a and a z, set this way by /import, read
// back byte for byte from /container/config/get.
func RouterOSEscaped(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s)*3 + 2)
	b.WriteByte('"')
	for i := range len(s) {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('\\')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0xF])
	}
	b.WriteByte('"')
	return b.String()
}

// registryCredential gives the router's /container/config the lab's registry
// credential, at every up and reset, when LAB_REGISTRY_USER and
// LAB_REGISTRY_TOKEN are set; without them it does nothing, and the router
// pulls anonymously. The snapshot never has it: provisioning takes the
// snapshot before any boot that gets here. Like the admin password, the
// credential reaches the router in a file (importPrivate), on no command
// line.
func (l *Lab) registryCredential(ctx context.Context) error {
	r := l.cfg.Registry
	if !r.Set() {
		return nil
	}
	redact := func(s string) string {
		s = Redact(s, RouterOSEscaped(r.Token), "<LAB_REGISTRY_TOKEN>")
		s = Redact(s, RouterOSEscaped(r.User), "<LAB_REGISTRY_USER>")
		s = Redact(s, r.Token, "<LAB_REGISTRY_TOKEN>")
		return Redact(s, r.User, "<LAB_REGISTRY_USER>")
	}
	if err := l.importPrivate(ctx, "registry.rsc", RegistryScript(r), "registry", "the registry credential", redact); err != nil {
		return err
	}
	l.sayf("/container/config: registry-url %s, username and password from LAB_REGISTRY_USER and LAB_REGISTRY_TOKEN", r.URL)
	return nil
}
