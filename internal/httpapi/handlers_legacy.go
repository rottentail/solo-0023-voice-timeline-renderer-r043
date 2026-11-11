package httpapi

import "net/http"

func (server *Server) handleLegacyList(w http.ResponseWriter, request *http.Request) {
	inventory, err := server.service.ListLegacy(request.Context(), principalFromHeaders(request))
	if err != nil {
		writeError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, inventory)
}

func (server *Server) handleLegacyAdopt(w http.ResponseWriter, request *http.Request) {
	manifest, err := server.service.AdoptLegacy(request.Context(), principalFromHeaders(request))
	if err != nil {
		writeError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, manifest)
}
