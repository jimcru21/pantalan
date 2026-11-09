package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/pantalan/pantalan/internal/domain"
)

const maxBodyBytes = 1 << 20

// ErrorResponse is the body of every JSON error answer. Field is set when
// the error is about one field of the request body (its JSON name, e.g.
// "domain"), so a form can show Error beside that field.
type ErrorResponse struct {
	Error string `json:"error"`
	Field string `json:"field,omitempty"`
}

func (h *handler) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.deps.Log.Warn("write response", "err", err)
	}
}

func (h *handler) writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, domain.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, domain.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, domain.ErrConflict):
		status = http.StatusConflict
	}
	body := ErrorResponse{Error: err.Error()}
	var fieldErr *domain.FieldError
	switch {
	case status == http.StatusInternalServerError:
		h.deps.Log.Error("request failed", "err", err)
		body.Error = "internal error" // don't leak internals (paths, command lines) to clients
	case errors.As(err, &fieldErr):
		// The message alone: it is shown beside the field it names.
		body = ErrorResponse{Error: fieldErr.Msg, Field: fieldErr.Field}
	}
	h.writeJSON(w, status, body)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && typeErr.Field != "" {
			name, _, _ := strings.Cut(typeErr.Field, ".") // "env.KEY" is about env
			return domain.InvalidField(name, fmt.Sprintf("%s must be %s", typeErr.Field, jsonKind(typeErr.Type)))
		}
		return fmt.Errorf("%w: %w", domain.ErrInvalid, err)
	}
	return nil
}

// jsonKind names a Go type the way a JSON client thinks of it.
func jsonKind(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Bool:
		return "true or false"
	case reflect.String:
		return "a string"
	case reflect.Slice, reflect.Array:
		return "a list"
	default:
		return "an object"
	}
}
