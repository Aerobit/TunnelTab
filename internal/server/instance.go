package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// RequestLaunchURL asks an already-running TunnelTab on port for a fresh
// login link, authenticating with the instance secret. It is used when the
// program is started a second time.
func RequestLaunchURL(port int, secret string) (string, error) {
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(port)+"/api/instance/launch", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set(instanceHeader, secret)
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("running instance answered %s", resp.Status)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.URL == "" {
		return "", fmt.Errorf("unexpected answer from running instance")
	}
	return out.URL, nil
}

// ConfirmStarted tells the TunnelTab on port, started by "Update now", that
// the previous version saw it answer and won't roll the update back. It
// fails until that instance serves requests, i.e. has opened its vault.
func ConfirmStarted(port int, secret string) error {
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(port)+"/api/instance/started", nil)
	if err != nil {
		return err
	}
	req.Header.Set(instanceHeader, secret)
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("new instance answered %s", resp.Status)
	}
	return nil
}
