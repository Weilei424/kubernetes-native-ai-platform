// control-plane/internal/mlflow/client.go
package mlflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client is the interface for interacting with the MLflow REST API.
type Client interface {
	// CreateRegisteredModel registers a model name. RESOURCE_ALREADY_EXISTS is silently ignored.
	CreateRegisteredModel(ctx context.Context, name string) error
	// CreateModelVersion creates a new version from the given artifact source.
	// Returns the version number and the artifact URI echoed by MLflow.
	CreateModelVersion(ctx context.Context, modelName, sourceURI, runID string) (versionNumber int, artifactURI string, err error)
	// SetModelAlias sets an alias on a specific version (MLflow v2 alias API).
	SetModelAlias(ctx context.Context, modelName, alias string, version int) error
	// DeleteModelAlias removes an alias. Missing aliases are silently ignored.
	DeleteModelAlias(ctx context.Context, modelName, alias string) error
	// GetModelVersionByAlias resolves an alias to a version number.
	GetModelVersionByAlias(ctx context.Context, modelName, alias string) (versionNumber int, err error)
}

// HTTPClient implements Client by calling the MLflow REST API.
type HTTPClient struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a new HTTPClient targeting the given MLflow tracking URI.
func New(trackingURI string) *HTTPClient {
	return &HTTPClient{
		baseURL:    strings.TrimRight(trackingURI, "/"),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

type mlflowError struct {
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
}

func (c *HTTPClient) doJSON(ctx context.Context, method, path string, reqBody, respBody interface{}) error {
	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode >= 400 {
		var mlErr mlflowError
		_ = json.Unmarshal(raw, &mlErr)
		return &mlflowError{ErrorCode: mlErr.ErrorCode, Message: mlErr.Message}
	}

	if respBody != nil {
		if err := json.Unmarshal(raw, respBody); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func (e *mlflowError) Error() string {
	return fmt.Sprintf("mlflow %s: %s", e.ErrorCode, e.Message)
}

func isAlreadyExists(err error) bool {
	var e *mlflowError
	if errors.As(err, &e) {
		return e.ErrorCode == "RESOURCE_ALREADY_EXISTS"
	}
	return false
}

// IsNotFound reports whether err is an MLflow "not found" error.
func IsNotFound(err error) bool {
	var e *mlflowError
	if errors.As(err, &e) {
		return e.ErrorCode == "RESOURCE_DOES_NOT_EXIST" || e.ErrorCode == "NOT_FOUND"
	}
	return false
}

func (c *HTTPClient) CreateRegisteredModel(ctx context.Context, name string) error {
	body := map[string]string{"name": name}
	err := c.doJSON(ctx, http.MethodPost, "/api/2.0/mlflow/registered-models/create", body, nil)
	if isAlreadyExists(err) {
		return nil
	}
	return err
}

func (c *HTTPClient) CreateModelVersion(ctx context.Context, modelName, sourceURI, runID string) (int, string, error) {
	reqBody := map[string]string{
		"name":   modelName,
		"source": sourceURI,
		"run_id": runID,
	}
	var resp struct {
		ModelVersion struct {
			Version string `json:"version"`
			Source  string `json:"source"`
		} `json:"model_version"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/2.0/mlflow/model-versions/create", reqBody, &resp); err != nil {
		return 0, "", err
	}
	vNum, err := strconv.Atoi(resp.ModelVersion.Version)
	if err != nil {
		return 0, "", fmt.Errorf("parse version number %q: %w", resp.ModelVersion.Version, err)
	}
	return vNum, resp.ModelVersion.Source, nil
}

func (c *HTTPClient) SetModelAlias(ctx context.Context, modelName, alias string, version int) error {
	reqBody := map[string]string{
		"name":    modelName,
		"alias":   alias,
		"version": strconv.Itoa(version),
	}
	return c.doJSON(ctx, http.MethodPatch, "/api/2.0/mlflow/registered-models/alias", reqBody, nil)
}

func (c *HTTPClient) DeleteModelAlias(ctx context.Context, modelName, alias string) error {
	reqBody := map[string]string{
		"name":  modelName,
		"alias": alias,
	}
	err := c.doJSON(ctx, http.MethodDelete, "/api/2.0/mlflow/registered-models/alias", reqBody, nil)
	if err != nil {
		if IsNotFound(err) {
			return nil
		}
	}
	return err
}

func (c *HTTPClient) GetModelVersionByAlias(ctx context.Context, modelName, alias string) (int, error) {
	params := url.Values{}
	params.Set("name", modelName)
	params.Set("alias", alias)
	path := "/api/2.0/mlflow/registered-models/alias?" + params.Encode()
	var resp struct {
		ModelVersion struct {
			Version string `json:"version"`
		} `json:"model_version"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return 0, err
	}
	vNum, err := strconv.Atoi(resp.ModelVersion.Version)
	if err != nil {
		return 0, fmt.Errorf("parse version number %q: %w", resp.ModelVersion.Version, err)
	}
	return vNum, nil
}
