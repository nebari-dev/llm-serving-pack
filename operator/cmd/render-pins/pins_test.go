package main

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestParsePins(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Pins
		wantErr string
	}{
		{"comments and blanks", "# c\n\nA=b\n  \n# d\nC=d=e\n", Pins{"A": "b", "C": "d=e"}, ""},
		{"empty", "", Pins{}, ""},
		{"quoted", "A=\"b\"\n", nil, "line 1"},
		{"single quoted", "# x\nA='b'\n", nil, "line 2"},
		{"space before =", "A =b\n", nil, "line 1"},
		{"space after =", "A= b\n", nil, "line 1"},
		{"dollar", "A=b\nB=$VAR\n", nil, "line 2"},
		{"duplicate", "A=b\nC=d\nA=e\n", nil, "line 3"},
		{"no equals", "A\n", nil, "line 1"},
		{"empty key", "=b\n", nil, "line 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePins(strings.NewReader(tt.in))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestRender(t *testing.T) {
	pins := Pins{"AI_GATEWAY_VERSION": "v1.1.0"}
	tests := []struct {
		name    string
		tmpl    string
		want    string
		wantErr string
	}{
		{"renders", "v: {{.AI_GATEWAY_VERSION}}\n", "# Generated from t/x.tmpl, do not edit.\nv: v1.1.0\n", ""},
		{"missing key", "v: {{.NOPE_VERSION}}\n", "", "NOPE_VERSION"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Render(strings.NewReader(tt.tmpl), "t/x.tmpl", pins)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

const collectorValues = `usage:
  customerSync:
    enabled: true
    interval: 30s
  collector:
    image:
      repository: ghcr.io/openmeterio/benthos-collector
      # v1.0.0-beta.234, pinned by digest.
      tag: "v1.0.0-beta.234@sha256:cdc35416af4ededfbdb531463a3135127274f595c4b4b44573e685e82aac4fc4"
      pullPolicy: IfNotPresent
    # port - the OTLP (gRPC) port.
    port: 4317
other:
  image:
    repository: keep
    tag: keep
`

const collectorWant = `usage:
  customerSync:
    enabled: true
    interval: 30s
  collector:
    image:
      repository: ghcr.io/x/y
      # v2, pinned by digest.
      tag: "v2@sha256:abc"
      pullPolicy: IfNotPresent
    # port - the OTLP (gRPC) port.
    port: 4317
other:
  image:
    repository: keep
    tag: keep
`

const omValues = `namespace: ""

# OpenMeter: the API.
# OpenMeter v1.0.0-beta.234 with app secrets encrypted at rest.
openmeter:
  image: ghcr.io/nebari-dev/openmeter:v1.0.0-beta.234-nebari.1@sha256:be47
  api:
    resources: {}
`

func TestSetImage(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		path    []string
		image   string
		want    string
		wantErr string
	}{
		{"collector block", collectorValues, []string{"usage", "collector", "image"}, "ghcr.io/x/y:v2@sha256:abc", collectorWant, ""},
		{"registry port without digest", collectorValues, []string{"usage", "collector", "image"}, "localhost:5000/x/y:v3",
			strings.NewReplacer("ghcr.io/x/y", "localhost:5000/x/y", "# v2, pinned by digest.", "# v3, pinned by digest.", `"v2@sha256:abc"`, `"v3"`).Replace(collectorWant), ""},
		{"scalar image", omValues, []string{"openmeter", "image"}, "ghcr.io/nebari-dev/openmeter:v2-nebari.1@sha256:ffff",
			strings.Replace(omValues, "v1.0.0-beta.234-nebari.1@sha256:be47", "v2-nebari.1@sha256:ffff", 1), ""},
		{"missing path", collectorValues, []string{"usage", "nope", "image"}, "a/b:c", "", "usage.nope.image"},
		{"no tag", collectorValues, []string{"usage", "collector", "image"}, "ghcr.io/x/y", "", "tag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SetImage([]byte(tt.in), tt.path, tt.image)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tt.want)
			}
			again, err := SetImage(got, tt.path, tt.image)
			if err != nil || string(again) != string(got) {
				t.Fatalf("second run changed output or failed: %v", err)
			}
		})
	}
}

func TestStale(t *testing.T) {
	pins := Pins{"V": "v1", "IMG": "ghcr.io/x/y:v2@sha256:abc"}
	fresh, err := Render(strings.NewReader("v: {{.V}}\n"), "tmpl/a.tmpl", pins)
	if err != nil {
		t.Fatal(err)
	}
	outputs := []Output{
		{Template: "tmpl/a.tmpl", Dest: "ex/a.yaml"},
		{Template: "tmpl/b.tmpl", Dest: "ex/b.yaml"},
	}
	images := []ImageField{{File: "values.yaml", Path: []string{"usage", "collector", "image"}, Key: "IMG"}}
	freshValues := collectorWant

	tests := []struct {
		name string
		fsys fstest.MapFS
		want []string
	}{
		{"one stale output", fstest.MapFS{
			"tmpl/a.tmpl": {Data: []byte("v: {{.V}}\n")}, "ex/a.yaml": {Data: fresh},
			"tmpl/b.tmpl": {Data: []byte("v: {{.V}}\n")}, "ex/b.yaml": {Data: []byte("old\n")},
			"values.yaml": {Data: []byte(freshValues)},
		}, []string{"ex/b.yaml"}},
		{"missing dest and stale image", fstest.MapFS{
			"tmpl/a.tmpl": {Data: []byte("v: {{.V}}\n")}, "ex/a.yaml": {Data: fresh},
			"tmpl/b.tmpl": {Data: []byte("v: {{.V}}\n")},
			"values.yaml": {Data: []byte(collectorValues)},
		}, []string{"ex/b.yaml", "values.yaml"}},
		{"all fresh", fstest.MapFS{
			"tmpl/a.tmpl": {Data: []byte("v: {{.V}}\n")}, "ex/a.yaml": {Data: fresh},
			"tmpl/b.tmpl": {Data: []byte("v: {{.V}}\n")}, "ex/b.yaml": {Data: func() []byte { b, _ := Render(strings.NewReader("v: {{.V}}\n"), "tmpl/b.tmpl", pins); return b }()},
			"values.yaml": {Data: []byte(freshValues)},
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Stale(tt.fsys, outputs, images, pins)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("missing template errors", func(t *testing.T) {
		if _, err := Stale(fstest.MapFS{}, outputs, nil, pins); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("missing key errors", func(t *testing.T) {
		if _, err := Stale(fstest.MapFS{"values.yaml": {Data: []byte(freshValues)}}, nil, images, Pins{}); err == nil {
			t.Fatal("want error")
		}
	})
}
