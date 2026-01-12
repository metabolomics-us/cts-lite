package main

import (
	"ctslite/api"
	"ctslite/model"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
)

// corsMiddleware adds CORS headers to HTTP responses
func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type")

		// Handle preflight OPTIONS request
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Call the next handler
		next(w, r)
	}
}

func main() {
	// Serve the frontend
	fs := http.FileServer(http.Dir("./web"))
	http.Handle("/", fs)

	// Load PubChemLite into memory
	datadir := os.Getenv("CTS_DATA_DIR")
	if datadir == "" {
		// For local development when not using the Docker image
		datadir = "./data"
	}
	dataset := path.Join(datadir, "PubChemLite_CCSbase_20250905_trimmed.csv")
	index, err := model.LoadPubChemLite(dataset)
	if err != nil {
		log.Fatalf("Error loading PubChemLite: %v", err)
	}

	// Default endpoints for health checks
	http.HandleFunc("/health", corsMiddleware(api.Status))
	http.HandleFunc("/status", corsMiddleware(api.Status))

	// Endpoint for matching against PubChemLite
	http.HandleFunc("/match", corsMiddleware(func(w http.ResponseWriter, r *http.Request) {
		api.Match(index, w, r)
	}))

	fmt.Println("Server launching on port 8080")
	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatalf("Error starting server: %v", err)
		return
	}
}
