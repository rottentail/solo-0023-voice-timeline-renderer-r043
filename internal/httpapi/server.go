package httpapi

import (
	"net/http"

	"voice-timeline-renderer/internal/service"
)

type Options struct {
	MaxSourceRequestBytes int64
	MaxJSONBytes          int64
}

type Server struct {
	service *service.Service
	options Options
	router  *http.ServeMux
}

func New(serviceLayer *service.Service, options Options) *Server {
	if options.MaxSourceRequestBytes <= 0 {
		options.MaxSourceRequestBytes = 48 << 20
	}
	if options.MaxJSONBytes <= 0 {
		options.MaxJSONBytes = 2 << 20
	}
	server := &Server{
		service: serviceLayer,
		options: options,
		router:  http.NewServeMux(),
	}
	server.routes()
	return server
}

func (server *Server) Handler() http.Handler {
	return withRecovery(withHeaders(server.router))
}

func (server *Server) routes() {
	server.router.HandleFunc("GET /healthz", server.handleHealth)

	// Project and membership administration.
	server.router.HandleFunc("PUT /v1/projects/{id}", server.handleProjectWrite)
	server.router.HandleFunc("GET /v1/projects", server.handleProjectList)
	server.router.HandleFunc("PUT /v1/memberships", server.handleMembershipWrite)
	server.router.HandleFunc("GET /v1/memberships/self", server.handleMembershipRead)

	// Cross-project read-only source grants.
	server.router.HandleFunc("PUT /v1/grants", server.handleGrantWrite)
	server.router.HandleFunc("GET /v1/grants", server.handleGrantList)
	server.router.HandleFunc("GET /v1/grants/{id}", server.handleGrantRead)
	server.router.HandleFunc("POST /v1/grants/{id}/revoke", server.handleGrantRevoke)

	// Legacy ownership migration.
	server.router.HandleFunc("GET /v1/legacy", server.handleLegacyList)
	server.router.HandleFunc("POST /v1/legacy/adopt", server.handleLegacyAdopt)

	// Project-scoped media workflows.
	server.router.HandleFunc("PUT /v1/sources/{id}", server.handleSourceWrite)
	server.router.HandleFunc("GET /v1/sources/{id}", server.handleSourceRead)
	server.router.HandleFunc("PUT /v1/timelines/{id}", server.handleTimelineWrite)
	server.router.HandleFunc("GET /v1/timelines/{id}", server.handleTimelineRead)
	server.router.HandleFunc("PUT /v1/renders/{id}", server.handleRenderWrite)
	server.router.HandleFunc("GET /v1/renders/{id}", server.handleRenderRead)
	server.router.HandleFunc("GET /v1/renders/{id}/receipt", server.handleReceiptRead)
	server.router.HandleFunc("GET /v1/renders/{id}/output", server.handleOutputRead)
}
