package httpapi

import (
	"net/http"

	"voice-timeline-renderer/internal/authz"
)

const (
	headerProjectID = "X-Project-Id"
	headerActorID   = "X-Actor-Id"
)

// principalFromHeaders extracts the authenticated project/actor identity. The
// service treats these headers as the verified identity supplied by the
// deployment's fronting authentication proxy; malformed or missing values are
// rejected by the authz guard rather than trusted here.
func principalFromHeaders(request *http.Request) authz.Principal {
	return authz.Principal{
		ProjectID: request.Header.Get(headerProjectID),
		ActorID:   request.Header.Get(headerActorID),
	}
}
