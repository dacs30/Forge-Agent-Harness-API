package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const maxBodySize = 1 << 20 // 1 MB

func decodeJSON(r *http.Request, dst any) error {
	body := http.MaxBytesReader(nil, r.Body, maxBodySize)
	defer body.Close()

	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	// Ensure no trailing content
	if decoder.More() {
		return fmt.Errorf("unexpected trailing content in request body")
	}

	return nil
}

// readBody reads the full request body up to maxBodySize.
func readBody(r *http.Request) ([]byte, error) {
	return readBodyMax(r, maxBodySize)
}

// readBodyMax reads the full request body up to the given limit in bytes.
func readBodyMax(r *http.Request, limit int64) ([]byte, error) {
	body := http.MaxBytesReader(nil, r.Body, limit)
	defer body.Close()
	return io.ReadAll(body)
}
