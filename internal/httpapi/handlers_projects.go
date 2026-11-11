package httpapi

import (
	"net/http"

	"voice-timeline-renderer/internal/service"
)

func (server *Server) handleProjectWrite(w http.ResponseWriter, request *http.Request) {
	var payload service.ProjectWriteRequest
	if err := decodeJSON(w, request, &payload, server.options.MaxJSONBytes); err != nil {
		writeError(w, err, nil)
		return
	}
	project, created, err := server.service.CreateProject(request.Context(), principalFromHeaders(request), request.PathValue("id"), payload)
	if err != nil {
		writeError(w, err, nil)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, project)
}

func (server *Server) handleProjectList(w http.ResponseWriter, request *http.Request) {
	projects, err := server.service.ListProjects(request.Context(), principalFromHeaders(request))
	if err != nil {
		writeError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (server *Server) handleMembershipWrite(w http.ResponseWriter, request *http.Request) {
	var payload service.MembershipWriteRequest
	if err := decodeJSON(w, request, &payload, server.options.MaxJSONBytes); err != nil {
		writeError(w, err, nil)
		return
	}
	membership, _, err := server.service.PutMembership(request.Context(), principalFromHeaders(request), payload)
	if err != nil {
		writeError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, membership)
}

func (server *Server) handleMembershipRead(w http.ResponseWriter, request *http.Request) {
	membership, err := server.service.GetMembership(request.Context(), principalFromHeaders(request))
	if err != nil {
		writeError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, membership)
}
