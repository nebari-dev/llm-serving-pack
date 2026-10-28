// Package openmeter sets up what the LLM serving pack needs in OpenMeter:
// a priced feature per model resource, and manual prices.
package openmeter

import (
	"github.com/nebari-dev/nebari-llm-serving-pack/shared/openmeterapi"
)

// Client calls OpenMeter's HTTP API.
type Client struct {
	*openmeterapi.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{openmeterapi.New(baseURL, token, nil)}
}
