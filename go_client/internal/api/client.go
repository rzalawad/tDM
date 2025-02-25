package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

type Client struct {
	baseURL string
}

type Download struct {
	ID         int    `json:"id"`
	URL        string `json:"url"`
	Status     string `json:"status"`
	Directory  string `json:"directory"`
	Speed      string `json:"speed"`
	Downloaded int    `json:"downloaded"`
	TotalSize  int    `json:"total_size"`
	DateAdded  string `json:"date_added"`
	Progress   string `json:"progress"`
	Error      string `json:"error"`
}

func NewClient(baseURL string) *Client {
	return &Client{baseURL: baseURL}
}

func (c *Client) FetchDownloads() ([]Download, error) {
	resp, err := http.Get(fmt.Sprintf("%s/downloads", c.baseURL))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var downloads []Download
	if err := json.NewDecoder(resp.Body).Decode(&downloads); err != nil {
		return nil, err
	}

	return downloads, nil
}

func (c *Client) FetchDownload(id int) (Download, error) {
	resp, err := http.Get(fmt.Sprintf("%s/download/%d", c.baseURL, id))
	if err != nil {
		return Download{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Download{}, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var download Download
	if err := json.NewDecoder(resp.Body).Decode(&download); err != nil {
		return Download{}, err
	}

	return download, nil
}

func (c *Client) SubmitDownload(url, directory string) error {
	downloadRequest := map[string]string{
		"url":       url,
		"directory": directory,
	}
	jsonData, err := json.Marshal(downloadRequest)
	if err != nil {
		return err
	}

	resp, err := http.Post(fmt.Sprintf("%s/download", c.baseURL), "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("failed to submit download, status code: %d", resp.StatusCode)
	}

	return nil
}
func (c *Client) UpdateConcurrency(concurrency int) error {
	concurrencyRequest := map[string]int{
		"concurrency": concurrency,
	}
	jsonData, err := json.Marshal(concurrencyRequest)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/settings/concurrency", c.baseURL), bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to update concurrency, status code: %d", resp.StatusCode)
	}

	return nil
}
func (c *Client) GetConcurrency() (int, error) {
	resp, err := http.Get(fmt.Sprintf("%s/settings/concurrency", c.baseURL))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result map[string]int
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	concurrency, ok := result["concurrency"]
	if !ok {
		return 0, fmt.Errorf("concurrency not found in response")
	}

	return concurrency, nil
}
