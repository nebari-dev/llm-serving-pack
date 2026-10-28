module github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server

go 1.26.5

require github.com/nebari-dev/nebari-llm-serving-pack/shared v0.0.0

require (
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	golang.org/x/sync v0.18.0 // indirect
)

replace github.com/nebari-dev/nebari-llm-serving-pack/shared => ../../shared
