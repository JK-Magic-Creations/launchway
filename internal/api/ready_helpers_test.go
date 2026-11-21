package api

import (
	"net/http"
	"net/http/httptest"
)

type jsonRecorder struct {
	*httptest.ResponseRecorder
	code int
	body map[string]any
}

func newRecorder() *jsonRecorder { return &jsonRecorder{ResponseRecorder: httptest.NewRecorder()} }

func requestFor(path string) *http.Request { return httptest.NewRequest(http.MethodGet, path, nil) }
