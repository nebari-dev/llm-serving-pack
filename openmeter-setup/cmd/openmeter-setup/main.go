// Command openmeter-setup runs the openmeter subchart's setup Jobs:
//
//	openmeter-setup bootstrap -config FILE.json   set up OpenMeter's features and prices, then exit
//	openmeter-setup ensure-secret -name N -key K  create Secret N with a random password at K, unless it exists
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/openmeter-setup/internal/kubesecret"
	"github.com/nebari-dev/nebari-llm-serving-pack/openmeter-setup/internal/openmeter"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "bootstrap":
		bootstrap(os.Args[2:])
	case "ensure-secret":
		ensureSecret(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: openmeter-setup bootstrap [-config FILE.json] | ensure-secret -name NAME [-key KEY]")
	os.Exit(2)
}

// ensureSecret creates a Secret with a random password in the pod's own
// namespace, once; an existing Secret is left as it is.
func ensureSecret(args []string) {
	fs := flag.NewFlagSet("ensure-secret", flag.ExitOnError)
	name := fs.String("name", "", "the Secret's name")
	key := fs.String("key", "password", "the key that holds the password")
	fs.Parse(args)
	if *name == "" {
		log.Fatal("ensure-secret: -name is required")
	}
	c, ns, err := kubesecret.InCluster()
	if err != nil {
		log.Fatalf("ensure-secret: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	created, err := c.Ensure(ctx, ns, *name, *key)
	if err != nil {
		log.Fatalf("ensure-secret: %v", err)
	}
	if created {
		log.Printf("ensure-secret: created %s/%s", ns, *name)
	} else {
		log.Printf("ensure-secret: %s/%s exists; left as it is", ns, *name)
	}
}

// bootstrap applies a BootstrapConfig, retrying while OpenMeter starts.
func bootstrap(args []string) {
	fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	omURL := fs.String("openmeter-url", cmp.Or(os.Getenv("OPENMETER_URL"), "http://localhost:8888"), "OpenMeter's API ($OPENMETER_URL); its token, if any, is $OPENMETER_TOKEN")
	path := fs.String("config", "/etc/openmeter-setup/bootstrap.json", "the features and prices to set up")
	timeout := fs.Duration("timeout", 5*time.Minute, "how long to keep trying")
	fs.Parse(args)
	b, err := os.ReadFile(*path)
	if err != nil {
		log.Fatal(err)
	}
	var cfg openmeter.BootstrapConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		log.Fatalf("%s: %v", *path, err)
	}
	c := openmeter.NewClient(*omURL, os.Getenv("OPENMETER_TOKEN"))
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	for {
		err := openmeter.Bootstrap(ctx, c, cfg, time.Now)
		if err == nil {
			log.Printf("bootstrap: %d model resources, %d manual prices in place", len(cfg.Resources), len(cfg.Prices))
			return
		}
		log.Printf("bootstrap: %v; retrying", err)
		select {
		case <-ctx.Done():
			log.Fatalf("bootstrap: gave up after %s", *timeout)
		case <-time.After(5 * time.Second):
		}
	}
}
