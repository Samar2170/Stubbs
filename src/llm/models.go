package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Model is an entry from the provider's model catalog.
type Model struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type modelsResponse struct {
	Data []Model `json:"data"`
}

// ListModels fetches the provider's available models.
func (c *ORClient) ListModels(ctx context.Context) ([]Model, error) {
	if c == nil {
		return nil, fmt.Errorf("client is nil")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, &APIError{Status: resp.StatusCode, Body: string(body)}
	}
	var mr modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, err
	}
	return mr.Data, nil
}
