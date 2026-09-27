//go:build labe2e

package lab

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	release, err := Prepare()
	if err != nil {
		fmt.Fprintln(os.Stderr, "lab e2e:", err)
		os.Exit(1)
	}
	code := m.Run()
	release()
	os.Exit(code)
}
