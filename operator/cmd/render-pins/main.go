/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Command render-pins writes every pinned dependency version from
// versions.env into the published examples and the chart image fields.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const pinFile = "versions.env"

// outputs are the published examples rendered from templates kept outside examples/.
var outputs = []Output{
	{Template: "operator/cmd/render-pins/templates/envoy-ai-gateway.yaml.tmpl", Dest: "examples/envoy-ai-gateway.yaml"},
	{Template: "operator/cmd/render-pins/templates/envoy-gateway.yaml.tmpl", Dest: "examples/envoy-gateway.yaml"},
}

// imageFields are the chart image values written from a single pin each.
var imageFields = []ImageField{
	{File: "charts/nebari-llm-serving/values.yaml", Path: []string{"usage", "collector", "image"}, Key: "USAGE_COLLECTOR_IMAGE"},
	{File: "charts/nebari-llm-serving/charts/nebari-openmeter/values.yaml", Path: []string{"openmeter", "image"}, Key: "OPENMETER_IMAGE"},
}

type usageError struct{ error }

func main() {
	os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr, outputs, imageFields))
}

func runMain(args []string, stdout, stderr io.Writer, outputs []Output, imageFields []ImageField) int {
	err := run(args, stdout, stderr, outputs, imageFields)
	var ue usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ue):
		fmt.Fprintln(stderr, "ERROR:", err)
		return 2
	default:
		fmt.Fprintln(stderr, "ERROR:", err)
		return 1
	}
}

func run(args []string, stdout, stderr io.Writer, outputs []Output, imageFields []ImageField) error {
	fl := flag.NewFlagSet("render-pins", flag.ContinueOnError)
	fl.SetOutput(stderr)
	root := fl.String("root", "", "path to the repository root")
	check := fl.Bool("check", false, "write nothing; exit 1 and list each stale file")
	if err := fl.Parse(args); err != nil {
		return usageError{err}
	}
	if *root == "" {
		return usageError{errors.New("--root is required")}
	}

	fsys := os.DirFS(*root)
	f, err := fsys.Open(pinFile)
	if err != nil {
		return fmt.Errorf("opening %s: %w", pinFile, err)
	}
	pins, err := ParsePins(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", pinFile, err)
	}

	stale, err := Stale(fsys, outputs, imageFields, pins)
	if err != nil {
		return err
	}
	if *check {
		for _, p := range stale {
			fmt.Fprintf(stdout, "STALE %s\n", p)
		}
		if len(stale) > 0 {
			return fmt.Errorf("%d generated file(s) stale; run: go run ./cmd/render-pins --root %s", len(stale), *root)
		}
		return nil
	}

	staleSet := map[string]bool{}
	for _, p := range stale {
		staleSet[p] = true
	}
	for _, o := range outputs {
		if !staleSet[o.Dest] {
			continue
		}
		data, err := renderOutput(fsys, o, pins)
		if err != nil {
			return err
		}
		if err := writeFile(*root, o.Dest, data); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "WROTE %s\n", o.Dest)
	}
	for _, img := range imageFields {
		if !staleSet[img.File] {
			continue
		}
		data, err := renderImage(fsys, img, pins)
		if err != nil {
			return err
		}
		if err := writeFile(*root, img.File, data); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "WROTE %s\n", img.File)
	}
	return nil
}

func writeFile(root, rel string, data []byte) error {
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	info, err := os.Stat(path)
	mode := os.FileMode(0o644)
	if err == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, data, mode)
}
