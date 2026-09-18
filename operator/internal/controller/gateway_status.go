package controller

import "time"

// Shared by model reconcilers when a required gateway CRD is unavailable.
const (
	reasonGatewayCRDUnavailable = "GatewayCRDUnavailable"
	gatewayCRDRequeue           = 15 * time.Second
)
