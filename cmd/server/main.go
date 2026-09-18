// Command server runs the GridWise LLM energy-optimisation API.
//
// Pipeline: LLM note interpretation -> deterministic guardrails -> exact LP
// -> replay validation. See README.md for the full architecture.
package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"gridwise/internal/httpapi"
	"gridwise/internal/interpret"
)

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func main() {
	interpreter := interpret.New()
	if !interpreter.Ready() {
		log.Printf("WARNING: no LLM provider configured (set GEMINI_API_KEY and/or GROQ_API_KEY)")
	} else {
		log.Printf("interpreter providers: %v", interpreter.ProviderNames())
	}

	port := env("PORT", "8000")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      httpapi.NewRouter(interpreter),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 35 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("gridwise listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("server error: %v", err)
		os.Exit(1)
	}
}
