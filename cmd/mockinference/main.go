// Command mockinference is a stand-in model server: it answers requests
// shaped like vLLM/OpenAI's completions API without loading any real
// weights, so InferenceService can be demonstrated end-to-end with $0 GPU
// budget. Swap InferenceService.spec.image for a real vLLM/Triton image to
// serve an actual model — the CRD/Deployment/Service wiring doesn't change.
package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"
)

type completionRequest struct {
	Prompt string `json:"prompt"`
}

type completionChoice struct {
	Text string `json:"text"`
}

type completionResponse struct {
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	model := envOr("MODEL", "mock-model")
	addr := envOr("LISTEN_ADDR", ":8000")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /v1/completions", func(w http.ResponseWriter, r *http.Request) {
		var req completionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(completionResponse{
			Model: model,
			Choices: []completionChoice{
				{Text: "[mock-inference/" + model + "] echo: " + req.Prompt},
			},
		})
	})

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	logger.Info("listening", "addr", addr, "model", model)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
