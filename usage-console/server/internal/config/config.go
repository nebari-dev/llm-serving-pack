// Package config reads the environment shared by every command.
package config

import "os"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

var (
	ConfigDB   = env("STARGATE_CONFIG_DB", "postgres://stargate:stargate@localhost:5433/stargate?sslmode=disable")
	ReceiptsDB = env("STARGATE_RECEIPTS_DB", "postgres://stargate:stargate@localhost:5434/receipts?sslmode=disable")
)
