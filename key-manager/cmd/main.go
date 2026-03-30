package main

import (
	"log"
	"net/http"
)

func main() {
	log.Println("key-manager starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", http.DefaultServeMux))
}
