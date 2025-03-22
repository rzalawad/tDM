package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

const defaultAria2RPCURL = "http://localhost:6800/jsonrpc"

// Duration represents a parsed duration like "1d", "2h", "30m"
type Duration struct {
	time.Duration
}

// ParseDuration parses duration strings like "1d", "2h", "30m", etc.
func ParseDuration(durationStr string) (time.Duration, error) {
	if durationStr == "" {
		return 24 * time.Hour, nil // default: 1 day
	}

	units := map[string]int64{
		"d": 86400,
		"h": 3600,
		"m": 60,
	}

	pattern := regexp.MustCompile(`(\d+)([dhm])`)
	matches := pattern.FindAllStringSubmatch(durationStr, -1)

	if len(matches) == 0 {
		return 24 * time.Hour, fmt.Errorf("invalid duration format: %s", durationStr)
	}

	var totalSeconds int64
	for _, match := range matches {
		value, _ := strconv.ParseInt(match[1], 10, 64)
		unit := match[2]
		totalSeconds += value * units[unit]
	}

	return time.Duration(totalSeconds) * time.Second, nil
}

// Aria2JsonRPC communicates with aria2c via JSON-RPC
type Aria2JsonRPC struct {
	rpcURL string
}

// NewAria2JsonRPC creates a new Aria2JsonRPC client
func NewAria2JsonRPC(rpcURL string) *Aria2JsonRPC {
	if rpcURL == "" {
		rpcURL = defaultAria2RPCURL
	}
	return &Aria2JsonRPC{rpcURL: rpcURL}
}

// RPCRequest represents a JSON-RPC request to aria2c
type RPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      string        `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

// RPCResponse represents a JSON-RPC response from aria2c
type RPCResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error,omitempty"`
}

// RPCError represents an error in a JSON-RPC response
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// callMethod makes a JSON-RPC call to aria2c
func (c *Aria2JsonRPC) callMethod(method string, params []interface{}) (json.RawMessage, error) {
	if params == nil {
		params = []interface{}{}
	}

	payload := RPCRequest{
		JSONRPC: "2.0",
		ID:      "aria2downloader",
		Method:  "aria2." + method,
		Params:  params,
	}

	log.Printf("Making RPC call to %s with method %s", c.rpcURL, method)

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON-RPC request: %w", err)
	}

	resp, err := http.Post(c.rpcURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to make JSON-RPC request: %w", err)
	}
	defer resp.Body.Close()

	var result RPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode JSON-RPC response: %w", err)
	}

	if result.Error != nil {
		return nil, fmt.Errorf("aria2 error: %s (code: %d)", result.Error.Message, result.Error.Code)
	}

	return result.Result, nil
}

// AddURI adds a download URI to aria2c
func (c *Aria2JsonRPC) AddURI(uri string, options map[string]string) (string, error) {
	var params []interface{}

	// Convert string options to interface{} map
	optionsInterface := make(map[string]interface{})
	for k, v := range options {
		optionsInterface[k] = v
	}

	params = []interface{}{[]string{uri}, optionsInterface}

	result, err := c.callMethod("addUri", params)
	if err != nil {
		return "", err
	}

	var gid string
	err = json.Unmarshal(result, &gid)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal AddURI result: %w", err)
	}

	return gid, nil
}

// GetStatus returns the status of a download by GID
func (c *Aria2JsonRPC) GetStatus(gid string) (map[string]interface{}, error) {
	params := []interface{}{gid}

	result, err := c.callMethod("tellStatus", params)
	if err != nil {
		return nil, err
	}

	var status map[string]interface{}
	err = json.Unmarshal(result, &status)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal status result: %w", err)
	}

	return status, nil
}

// GetGlobalStat returns global download stats from aria2c
func (c *Aria2JsonRPC) GetGlobalStat() (map[string]interface{}, error) {
	result, err := c.callMethod("getGlobalStat", nil)
	if err != nil {
		return nil, err
	}

	var stats map[string]interface{}
	err = json.Unmarshal(result, &stats)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal global stats: %w", err)
	}

	return stats, nil
}

// Pause pauses a download by GID
func (c *Aria2JsonRPC) Pause(gid string) error {
	params := []interface{}{gid}

	_, err := c.callMethod("pause", params)
	return err
}

// Unpause resumes a paused download by GID
func (c *Aria2JsonRPC) Unpause(gid string) error {
	params := []interface{}{gid}

	_, err := c.callMethod("unpause", params)
	return err
}

// Remove removes a download by GID
func (c *Aria2JsonRPC) Remove(gid string) error {
	params := []interface{}{gid}

	_, err := c.callMethod("remove", params)
	return err
}

// Sets the concurrency for aria2c
func (c *Aria2JsonRPC) SetConcurrency(concurrency int) error {
	params := []any{map[string]any{"max-concurrent-downloads": strconv.Itoa(concurrency)}}

	_, err := c.callMethod("changeGlobalOption", params)
	return err
}

// IsAria2Running checks if aria2c RPC server is running
func IsAria2Running() bool {
	client := NewAria2JsonRPC("")
	_, err := client.GetGlobalStat()
	return err == nil
}
