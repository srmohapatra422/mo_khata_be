package uploads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/httpx"
)

const (
	LogoBucket  = "shop-logos"
	MaxLogoSize = 5 << 20 // 5 MB
)

var logoTypes = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
}

// Storage talks to Supabase Storage with the service key (server side only).
type Storage struct {
	BaseURL    string // e.g. https://<ref>.supabase.co/storage/v1
	ServiceKey string
	client     *http.Client
}

func NewStorage(baseURL, serviceKey string) *Storage {
	return &Storage{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		ServiceKey: serviceKey,
		client:     &http.Client{Timeout: 20 * time.Second},
	}
}

// PublicPrefix is the start of every public logo URL.
func (s *Storage) PublicPrefix() string {
	return s.BaseURL + "/object/public/" + LogoBucket + "/"
}

func (s *Storage) do(ctx context.Context, method, path, contentType string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.ServiceKey)
	req.Header.Set("apikey", s.ServiceKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return s.client.Do(req)
}

// EnsureBucket creates the public logo bucket when it does not exist yet.
func (s *Storage) EnsureBucket(ctx context.Context) error {
	body, _ := json.Marshal(map[string]any{
		"id": LogoBucket, "name": LogoBucket, "public": true,
		"file_size_limit":    MaxLogoSize,
		"allowed_mime_types": []string{"image/png", "image/jpeg", "image/webp"},
	})
	resp, err := s.do(ctx, http.MethodPost, "/bucket", "application/json", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	switch {
	case resp.StatusCode < 300:
		return nil
	case strings.Contains(strings.ToLower(string(msg)), "already exists"), resp.StatusCode == http.StatusConflict:
		return nil
	}
	return fmt.Errorf("create bucket: %s: %s", resp.Status, msg)
}

func (s *Storage) put(ctx context.Context, key, contentType string, data []byte) error {
	resp, err := s.do(ctx, http.MethodPost, "/object/"+LogoBucket+"/"+key, contentType, data)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("upload: %s: %s", resp.Status, msg)
	}
	return nil
}

type Handler struct {
	Storage *Storage // nil when SUPABASE_SERVICE_KEY is not set
}

// POST /v1/uploads/logo — multipart/form-data, field "file". Token only (no shop needed yet).
func (h Handler) Logo(w http.ResponseWriter, r *http.Request) {
	if h.Storage == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "logo upload is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxLogoSize+(1<<20)) // room for the form overhead
	f, _, err := r.FormFile("file")
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "image is larger than 5 MB")
			return
		}
		httpx.Error(w, http.StatusBadRequest, `send the image as multipart/form-data in the field "file" (max 5 MB)`)
		return
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, MaxLogoSize+1))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "could not read the file")
		return
	}
	if len(data) == 0 {
		httpx.Error(w, http.StatusBadRequest, "empty file")
		return
	}
	if len(data) > MaxLogoSize {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "image is larger than 5 MB")
		return
	}
	// Trust the bytes, not the file name or the client's content type.
	ctype := http.DetectContentType(data)
	ext, ok := logoTypes[ctype]
	if !ok {
		httpx.Error(w, http.StatusUnsupportedMediaType, "only PNG, JPEG or WebP images are allowed")
		return
	}

	key := fmt.Sprintf("%s/%s.%s", auth.UserID(r.Context()), uuid.NewString(), ext)
	if err := h.Storage.put(r.Context(), key, ctype, data); err != nil {
		log.Printf("logo upload failed: %v", err)
		httpx.Error(w, http.StatusBadGateway, "could not store the image")
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"url": h.Storage.PublicPrefix() + key})
}
