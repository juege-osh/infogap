package main

import "net/http"

type supportResponse struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *supportResponse) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.status = status
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
}
func (w *supportResponse) Write(data []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *supportResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
