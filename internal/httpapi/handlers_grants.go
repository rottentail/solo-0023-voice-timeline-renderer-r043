package httpapi

import (
	"net/http"

	"voice-timeline-renderer/internal/service"
)

func (server *Server) handleGrantWrite(w http.ResponseWriter, request *http.Request) {
	var payload service.GrantWriteRequest
	if err := decodeJSON(w, request, &payload, server.options.MaxJSONBytes); err != nil {
		writeError(w, err, nil)
		return
	}
	grant, _, err := server.service.CreateGrant(request.Context(), principalFromHeaders(request), payload)
	if err != nil {
		writeError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusCreated, grant)
}

func (server *Server) handleGrantList(w http.ResponseWriter, request *http.Request) {
	grants, err := server.service.ListGrants(request.Context(), principalFromHeaders(request))
	if err != nil {
		writeError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"grants": grants})
}

func (server *Server) handleGrantRead(w http.ResponseWriter, request *http.Request) {
	grant, err := server.service.GetGrant(request.Context(), principalFromHeaders(request), request.PathValue("id"))
	if err != nil {
		writeError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, grant)
}

func (server *Server) handleGrantRevoke(w http.ResponseWriter, request *http.Request) {
	grant, err := server.service.RevokeGrant(request.Context(), principalFromHeaders(request), request.PathValue("id"))
	if err != nil {
		writeError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, grant)
}
