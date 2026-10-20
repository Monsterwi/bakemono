package server

import "net/http"

// ServeHTTP lets embedders use the same cache handler as the standalone server.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler(w, r)
}
