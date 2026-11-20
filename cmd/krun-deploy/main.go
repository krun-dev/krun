// krun-deploy renders private Kubernetes artifacts from the main configuration.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/krun-dev/krun/internal/adapters/deployment"
	"github.com/krun-dev/krun/internal/config"
)

func main() {
	file := flag.String("config", "", "Main application YAML (required)")
	gateway := flag.String("check-gateway", "", "Check a complete Gateway List JSON instead of rendering")
	runtimeOnly := flag.Bool("runtime-only", false, "Render serving Runtime Deployments as YAML")
	out := flag.String("out", "", "Private output directory (required; contains Secrets)")
	flag.Parse()
	cfg, err := config.LoadConfig(*file)
	if err == nil {
		if *gateway != "" {
			var raw []byte
			raw, err = os.ReadFile(*gateway)
			if err == nil {
				err = deployment.CheckGateways(cfg, raw)
			}
		} else if *runtimeOnly {
			err = deployment.RenderRuntimeDeployments(cfg, *out)
		} else {
			err = deployment.Render(cfg, *out)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *runtimeOnly {
		fmt.Println("Rendered runtime-deployments.yaml; no cluster changes made")
		return
	}
	if *gateway != "" {
		fmt.Println("Gateway listener, namespace allowance and readiness verified")
		return
	}
	fmt.Println("Rendered resources.json (private) and summary.json; no cluster or cloud changes made")
}
