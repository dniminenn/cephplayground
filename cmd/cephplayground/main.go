package main

import (
	"os"

	"github.com/dniminenn/cephplayground/internal/adm"
)

func main() {
	os.Exit(adm.Main(os.Args[1:], os.Stdout, os.Stderr))
}
