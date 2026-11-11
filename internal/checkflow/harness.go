package checkflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"voice-timeline-renderer/internal/authz"
	"voice-timeline-renderer/internal/httpapi"
	"voice-timeline-renderer/internal/service"
	"voice-timeline-renderer/internal/storage"
)

type Principal = authz.Principal

type Harness struct {
	baseURL string
	client  *http.Client
	tempDir string
	server  *httptest.Server
	repo    *storage.Repository
	guard   *authz.Guard
	service *service.Service
}

func NewHarness() (*Harness, error) {
	return newHarness(service.DefaultConfig(), nil)
}

// NewHarnessWithConfig builds a harness with a custom service configuration so
// workflow checks can install a pre-finalize hook and exercise revocation races
// deterministically.
func NewHarnessWithConfig(config service.Config) (*Harness, error) {
	return newHarness(config, nil)
}

// NewHarnessOverRepository builds a second service/HTTP stack over an existing
// repository, used when a revocation hook must observe the same durable state
// as the primary workflow harness.
func NewHarnessOverRepository(repo *storage.Repository, config service.Config) (*Harness, error) {
	return newHarness(config, repo)
}

func newHarness(config service.Config, sharedRepo *storage.Repository) (*Harness, error) {
	tempDir := ""
	var repo *storage.Repository
	var err error
	if sharedRepo != nil {
		repo = sharedRepo
	} else {
		tempDir, err = os.MkdirTemp("", "voice-timeline-check-*")
		if err != nil {
			return nil, err
		}
		repo, err = storage.NewRepository(tempDir)
		if err != nil {
			_ = os.RemoveAll(tempDir)
			return nil, err
		}
	}
	guard := authz.NewGuard(repo)
	serviceLayer := service.New(repo, guard, config)
	httpServer := httpapi.New(serviceLayer, httpapi.Options{
		MaxSourceRequestBytes: 48 << 20,
		MaxJSONBytes:          2 << 20,
	})
	server := httptest.NewServer(httpServer.Handler())
	return &Harness{
		baseURL: server.URL,
		client:  &http.Client{Timeout: 45 * time.Second},
		tempDir: tempDir,
		server:  server,
		repo:    repo,
		guard:   guard,
		service: serviceLayer,
	}, nil
}

// Repository exposes the backing repository so workflow checks can stage
// pre-isolation legacy data directly.
func (harness *Harness) Repository() *storage.Repository {
	return harness.repo
}

// Service exposes the service layer for in-process revocation race checks.
func (harness *Harness) Service() *service.Service {
	return harness.service
}

func (harness *Harness) Close() {
	harness.server.Close()
	if harness.tempDir != "" {
		_ = os.RemoveAll(harness.tempDir)
	}
}

func (harness *Harness) requestJSON(
	ctx context.Context,
	method string,
	path string,
	payload any,
) (int, []byte, error) {
	return harness.requestJSONAs(ctx, method, path, Principal{}, payload)
}

func (harness *Harness) requestJSONAs(
	ctx context.Context,
	method string,
	path string,
	principal Principal,
	payload any,
) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, harness.baseURL+path, body)
	if err != nil {
		return 0, nil, err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if principal.ProjectID != "" {
		request.Header.Set("X-Project-Id", principal.ProjectID)
	}
	if principal.ActorID != "" {
		request.Header.Set("X-Actor-Id", principal.ActorID)
	}
	response, err := harness.client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, nil, err
	}
	return response.StatusCode, result, nil
}

func decodeBody(body []byte, target any) error {
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode response %q: %w", string(body), err)
	}
	return nil
}
