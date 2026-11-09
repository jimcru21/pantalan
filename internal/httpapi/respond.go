package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
	"github.com/pantalan/pantalan/internal/service"
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

// errorStatuses are the errors that answer with a status of their own;
// any other error is a 500. The sign-in errors answer with their own
// message alone (bare), whatever wraps them: a wait, say, goes in
// Retry-After instead.
var errorStatuses = []struct {
	err    error
	status int
	bare   bool
}{
	{domain.ErrNotFound, http.StatusNotFound, false},
	{domain.ErrInvalid, http.StatusBadRequest, false},
	{domain.ErrConflict, http.StatusConflict, false},
	{service.ErrBadCredentials, http.StatusUnauthorized, true},
	{service.ErrNoSession, http.StatusUnauthorized, true},
	{service.ErrNoAdmin, http.StatusForbidden, true},
	{service.ErrThrottled, http.StatusTooManyRequests, true},
	{service.ErrBusy, http.StatusServiceUnavailable, true},
}

// writeError answers err as an ErrorResponse with its status (see
// errorStatuses), and Retry-After when it is a service.RetryLaterError.
func (h *handler) writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	body := ErrorResponse{Error: err.Error()}
	for _, e := range errorStatuses {
		if errors.Is(err, e.err) {
			status = e.status
			if e.bare {
				body.Error = e.err.Error()
			}
			break
		}
	}
	var fieldErr *domain.FieldError
	var later *service.RetryLaterError
	switch {
	case status == http.StatusInternalServerError:
		h.deps.Log.Error("request failed", "err", err)
		body.Error = "internal error" // don't leak internals (paths, command lines) to clients
	case errors.As(err, &fieldErr):
		// The message alone: it is shown beside the field it names.
		body = ErrorResponse{Error: fieldErr.Msg, Field: fieldErr.Field}
	case errors.As(err, &later):
		w.Header().Set("Retry-After", retryAfter(later.Wait))
	}
	h.writeJSON(w, status, body)
}

// retryAfter is a Retry-After value: whole seconds, rounded up.
func retryAfter(wait time.Duration) string {
	return strconv.Itoa(int(math.Ceil(wait.Seconds())))
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
