package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

func (a *App) clashBase() string {
	return "http://127.0.0.1:" + itoa(a.store.Settings.ClashPort)
}

func (a *App) clashReq(method, path string, body any) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.clashBase()+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if sec := a.store.Settings.Secret; sec != "" {
		req.Header.Set("Authorization", "Bearer "+sec)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return data, resp.StatusCode, nil
}

// DelayTest mengetes latency satu proxy lewat clash API. Mengembalikan ms atau -1.
func (a *App) DelayTest(proxyName, testURL string) int {
	if testURL == "" {
		testURL = a.healthURL()
	}
	p := "/proxies/" + url.PathEscape(proxyName) + "/delay?timeout=5000&url=" + url.QueryEscape(testURL)
	data, code, err := a.clashReq("GET", p, nil)
	if err != nil || code >= 400 {
		return -1
	}
	var r struct {
		Delay int `json:"delay"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return -1
	}
	return r.Delay
}

// SelectProxy memilih node aktif pada sebuah selector group.
func (a *App) SelectProxy(group, name string) error {
	p := "/proxies/" + url.PathEscape(group)
	_, code, err := a.clashReq("PUT", p, map[string]string{"name": name})
	if err != nil {
		return err
	}
	if code >= 400 {
		return fmt.Errorf("clash api http %d", code)
	}
	return nil
}
