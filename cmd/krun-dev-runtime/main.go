// krun-dev-runtime renders development Kubernetes resources from LoadConfig.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/krun-dev/krun/internal/adapters/devruntime"
	"github.com/krun-dev/krun/internal/config"
)

func main() {
	file := flag.String("config", ".data/dev.yaml", "Main application configuration")
	out := flag.String("out", "", "Private output directory")
	host := flag.String("control-host", "", "Cluster-reachable control-plane IPv4 address")
	check := flag.Bool("check-storage", false, "Check configured S3 bucket access before dev-up")
	flag.Parse()
	c, err := config.LoadConfig(*file)
	if err == nil {
		if *check {
			err = devruntime.CheckStorage(c.Workers)
		} else {
			err = devruntime.Render(c, *host, *out)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
