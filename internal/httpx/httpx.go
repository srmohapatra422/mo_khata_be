package httpx

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
)

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}

func Decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// Err is an error that maps straight to an HTTP status and an {"error": ...} body.
type Err struct {
	Status int
	Msg    string
}

func (e *Err) Error() string { return e.Msg }

func BadRequest(msg string) *Err { return &Err{http.StatusBadRequest, msg} }
func Forbidden(msg string) *Err  { return &Err{http.StatusForbidden, msg} }
func NotFound(msg string) *Err   { return &Err{http.StatusNotFound, msg} }
func Conflict(msg string) *Err   { return &Err{http.StatusConflict, msg} }

// Fail writes err as a response: an *Err keeps its status, anything else is a 500.
func Fail(w http.ResponseWriter, err error) {
	var he *Err
	if errors.As(err, &he) {
		Error(w, he.Status, he.Msg)
		return
	}
	log.Printf("internal error: %v", err)
	Error(w, http.StatusInternalServerError, "database error")
}
