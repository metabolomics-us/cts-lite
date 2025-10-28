package main

import (
	"ctslite/api"
	"fmt"
	"log"
	"net/http"
)

func main() {
	// Load PubChemLite into memory
	filePath := "./pubchemlite_data/PubChemLite_CCSbase_20250905.csv"
	fmt.Printf("Loading PubChemLite into memory using %v...\n", filePath)
	index, err := api.LoadPubChemLite(filePath)
	if err != nil {
		log.Fatalf("Error loading PubChemLite: %v. Tried using the following file: %v", err, filePath)
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
