// Command companion is the companion with every building block of this
// repository, as the full image has it, for tests that run it as a process
// (a restart in the middle of a job).
package main

import (
	"github.com/LNA-DEV/HomePageCompanion/core"
	_ "github.com/LNA-DEV/HomePageCompanion/e2e"
)

func main() { core.Main() }
