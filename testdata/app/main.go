// Command app is a fixture the workflows of this repository release: a module of its own with
// a dependency, so that notices gathers a real module's license.
package main

import (
	"fmt"

	"example.com/lib"
)

const version = "1.2.3"

func main() { fmt.Println(lib.Name(), version) }
