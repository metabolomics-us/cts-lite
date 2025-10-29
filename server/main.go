package main

import (
	"ctslite/api"
	"ctslite/data"
	"fmt"
	"log"
	"net/http"
)

func main() {
	// Load PubChemLite into memory
	file := "./data/PubChemLite_CCSbase_20250905.csv"
	index, err := data.LoadPubChemLite(file)
	if err != nil {
		log.Fatalf("Error loading PubChemLite: %v", err)
	}
	fmt.Printf("Loaded %d compounds\n", len(index.Compounds))

	// Default endpoints for health checks
	http.HandleFunc("/", api.Status)
	http.HandleFunc("/health", api.Status)
	http.HandleFunc("/status", api.Status)

	// Endpoints for matching against PubChemLite
	http.HandleFunc("/match", func(w http.ResponseWriter, r *http.Request) {
		api.Match(index, w, r)
	})

	fmt.Println("Server launching on port 8080")
	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatalf("Error starting server: %v", err)
		return
	}
}
