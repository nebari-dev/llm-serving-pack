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

package imagecheck

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseDefaultImages(t *testing.T) {
	t.Parallel()

	values := []byte(`
keyManager:
  image:
    repository: ghcr.io/example/key-manager
    tag: "latest"
    pullPolicy: Always
defaults:
  serving:
    image: ghcr.io/example/serving:v1.2.3
  epp:
    image: ghcr.io/example/epp@sha256:1234
`)

	got, err := ParseDefaultImages(values)
	if err != nil {
		t.Fatalf("ParseDefaultImages() unexpected error: %v", err)
	}
	want := []DefaultImage{
		{Path: "defaults.epp.image", Reference: "ghcr.io/example/epp@sha256:1234"},
		{Path: "defaults.serving.image", Reference: "ghcr.io/example/serving:v1.2.3"},
		{Path: "keyManager.image", Reference: "ghcr.io/example/key-manager:latest"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseDefaultImages() = %#v, want %#v", got, want)
	}
}

func TestParseDefaultImagesSkipsReleasePinnedMappings(t *testing.T) {
	t.Parallel()

	values := []byte(`
openmeter:
  setup:
    image:
      tag: ""
  console:
    image:
      tag: ""
component:
  image:
    repository: ghcr.io/example/component
    tag: ""
operator:
  image:
    repository: ghcr.io/example/operator
    tag: "latest"
`)

	got, err := ParseDefaultImages(values)
	if err != nil {
		t.Fatalf("ParseDefaultImages() unexpected error: %v", err)
	}
	want := []DefaultImage{
		{Path: "component.image", SkipReason: "empty tag"},
		{Path: "openmeter.console.image", SkipReason: "empty tag"},
		{Path: "openmeter.setup.image", SkipReason: "empty tag"},
		{Path: "operator.image", Reference: "ghcr.io/example/operator:latest"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseDefaultImages() = %#v, want %#v", got, want)
	}
}

func TestParseDefaultImagesRejectsOnlySkippedMappings(t *testing.T) {
	t.Parallel()

	_, err := ParseDefaultImages([]byte("component:\n  image:\n    tag: \"\"\n"))
	if err == nil || !strings.Contains(err.Error(), "no image defaults") {
		t.Fatalf("ParseDefaultImages() error = %v, want no image defaults", err)
	}
}

func TestParseDefaultImagesRejectsIncompleteImageMapping(t *testing.T) {
	t.Parallel()

	_, err := ParseDefaultImages([]byte(`component:
  image:
    repository: ghcr.io/example/component
`))
	if err == nil || !strings.Contains(err.Error(), "component.image.tag") {
		t.Fatalf("ParseDefaultImages() error = %v, want missing component.image.tag", err)
	}
}

func TestParseDefaultImagesRejectsMappingWithoutRepository(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values string
		want   string
	}{
		{
			name: "misspelled key",
			values: `
operator:
  image: ghcr.io/example/operator:latest
component:
  image:
    repositry: ghcr.io/example/component
    tag: "v1"
`,
			want: "component.image.repository is required",
		},
		{
			name: "missing key",
			values: `
operator:
  image: ghcr.io/example/operator:latest
component:
  image:
    tag: "v1"
`,
			want: "component.image.repository is required",
		},
		{
			name: "empty value",
			values: `
operator:
  image: ghcr.io/example/operator:latest
component:
  image:
    repository: ""
    tag: "v1"
`,
			want: "component.image.repository must be a non-empty string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseDefaultImages([]byte(tt.values))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseDefaultImages() error = %v, want %s", err, tt.want)
			}
		})
	}
}

func TestParseDefaultImagesRejectsEmptySet(t *testing.T) {
	t.Parallel()

	_, err := ParseDefaultImages([]byte("feature:\n  enabled: true\n"))
	if err == nil || !strings.Contains(err.Error(), "no image defaults") {
		t.Fatalf("ParseDefaultImages() error = %v, want no image defaults", err)
	}
}

func TestParseDefaultImagesRejectsInvalidYAML(t *testing.T) {
	t.Parallel()

	_, err := ParseDefaultImages([]byte("component: [\n"))
	if err == nil || !strings.Contains(err.Error(), "parsing values YAML") {
		t.Fatalf("ParseDefaultImages() error = %v, want YAML parse error", err)
	}
}
