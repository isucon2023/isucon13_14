package isupipe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type InitializeResponse struct {
	Language string `json:"language"`
}

func (c *Client) Initialize(ctx context.Context) (*InitializeResponse, error) {
	req, err := c.agent.NewRequest(http.MethodPost, "/api/initialize", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Content-Type", "application/json;charset=utf-8")

	resp, err := c.agent.Do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("initializeのリクエストに失敗しました %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("initializeのステータスが200ではありません")
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	var initializeResp *InitializeResponse
	if json.NewDecoder(resp.Body).Decode(&initializeResp); err != nil {
		return nil, fmt.Errorf("initializeのJSONのdecodeに失敗しました %v", err)
	}

	return initializeResp, nil
}
