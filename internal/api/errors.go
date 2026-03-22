package api

import (
	"encoding/json"
	"net/http"
)

type ErrorResponse struct {
	Error  string `json:"error"`
	Code   int    `json:"code"`
	Detail string `json:"detail,omitempty"`
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, ErrorResponse{
		Error: msg,
		Code:  code,
	})
}

func writeErrorDetail(w http.ResponseWriter, code int, msg, detail string) {
	writeJSON(w, code, ErrorResponse{
		Error:  msg,
		Code:   code,
		Detail: detail,
	})
}

func writeJSON(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(data)
}
