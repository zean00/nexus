package httpx

import (
	"encoding/json"
	"net/http"
)

type Envelope struct {
	Data any `json:"data,omitempty"`
	Meta any `json:"meta,omitempty"`
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func Respond(w http.ResponseWriter, status int, data, meta any) {
	JSON(w, status, Envelope{Data: data, Meta: meta})
}

func OK(w http.ResponseWriter, data, meta any) {
	Respond(w, http.StatusOK, data, meta)
}

func Accepted(w http.ResponseWriter, data, meta any) {
	Respond(w, http.StatusAccepted, data, meta)
}

func Page(w http.ResponseWriter, status int, items any, nextCursor string, totalCount int) {
	JSON(w, status, map[string]any{
		"items":       items,
		"next_cursor": nextCursor,
		"total_count": totalCount,
	})
}

func Error(w http.ResponseWriter, status int, message string) {
	JSON(w, status, map[string]string{"error": message})
}
