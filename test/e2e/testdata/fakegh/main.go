// Command fakegh stands in for the GitHub CLI in tests: it prints the token
// it was given and its arguments.
package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	fmt.Printf("fakegh GH_TOKEN=%s GITHUB_TOKEN=%s args=%s\n", os.Getenv("GH_TOKEN"), os.Getenv("GITHUB_TOKEN"), strings.Join(os.Args[1:], " "))
}
