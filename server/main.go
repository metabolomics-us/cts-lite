package main

import (
	"context"
	"ctslite/api"
	"ctslite/model"
	"fmt"
	"log"
	"net/http"
	"os"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// corsMiddleware adds CORS headers to HTTP responses
func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, X-CTSL-Client")

		// Handle preflight OPTIONS request
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Call the next handler
		next(w, r)
	}
}

func serveDoc(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "./web/pages/docs.html")
}

func main() {
	// Initialize OpenTelemetry (traces, metrics, logs)
	// Observability must never block startup, so on error we log and continue
	otelShutdown, err := setupTelemetry(context.Background())
	if err != nil {
		log.Printf("OpenTelemetry setup failed, continuing without telemetry: %v", err)
	}
	// Gracefully shutdown OpenTelemetry on exit
	defer func() {
		if err := otelShutdown(context.Background()); err != nil {
			log.Printf("OpenTelemetry shutdown error: %v", err)
		}
	}()

	// Serve the frontend
	fs := http.FileServer(http.Dir("./web"))
	http.Handle("/", fs)

	// Clean URLs for documentation page
	http.HandleFunc("/docs", serveDoc)
	http.HandleFunc("/documentation", serveDoc)

	// Redirect legacy path to canonical URL
	http.HandleFunc("/pages/documentation.html", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs", http.StatusMovedPermanently)
	})
	http.HandleFunc("/pages/docs.html", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs", http.StatusMovedPermanently)
	})

	dbPath := "dataset/compounds.db"
	if envPath := os.Getenv("DB_PATH"); envPath != "" {
		dbPath = envPath
	}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		log.Fatalf("Database file %s does not exist", dbPath)
	}

	index, err := model.OpenSQLiteIndex(dbPath)
	if err != nil {
		log.Fatalf("Error opening SQLite index: %v", err)
	}

	// Default endpoints for health checks
	http.HandleFunc("/health", corsMiddleware(api.Status))
	http.HandleFunc("/status", corsMiddleware(api.Status))

	// Endpoint for matching against database
	matchHandler := corsMiddleware(func(w http.ResponseWriter, r *http.Request) {
		api.Match(index, w, r)
	})
	http.Handle("/match", otelhttp.NewHandler(matchHandler, "match"))

	port := ":8080"
	if p := os.Getenv("PORT"); p != "" {
		port = ":" + p
	}
	fmt.Printf("Server launching on port %s\n", port[1:])
	err = http.ListenAndServe(port, nil)
	if err != nil {
		log.Fatalf("Error starting server: %v", err)
		return
	}
}
