package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/util/common"
)

// WarpService provides business logic for Cloudflare WARP integration.
// It manages WARP configuration and connectivity settings.
type WarpService struct {
	SettingService
}

func (s *WarpService) GetWarpData() (string, error) {
	warp, err := s.SettingService.GetWarp()
	if err != nil {
		return "", err
	}
	return warpDataForBrowser(warp)
}

// warpDataForBrowser removes server-only Cloudflare credentials and the private
// key before the stored WARP record is returned to the admin UI. The key is only
// sent separately when the operator explicitly asks to build the WARP outbound.
func warpDataForBrowser(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return "", err
	}
	delete(data, "access_token")
	delete(data, "license_key")
	delete(data, "private_key")
	encoded, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// warpConfigForBrowser returns only the peer/interface config consumed by the
// frontend, not the Cloudflare registration envelope containing account tokens.
func warpConfigForBrowser(raw string) (string, error) {
	var response map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		return "", err
	}
	config, ok := response["config"]
	if !ok || len(config) == 0 {
		return "", fmt.Errorf("Cloudflare WARP response has no config")
	}
	encoded, err := json.Marshal(struct {
		Config json.RawMessage `json:"config"`
	}{Config: config})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func warpConfigWithPrivateKeyForBrowser(raw, privateKey string) (string, error) {
	if privateKey == "" {
		return "", fmt.Errorf("stored WARP private key is unavailable")
	}
	filtered, err := warpConfigForBrowser(raw)
	if err != nil {
		return "", err
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal([]byte(filtered), &response); err != nil {
		return "", err
	}
	keyJSON, err := json.Marshal(privateKey)
	if err != nil {
		return "", err
	}
	response["private_key"] = keyJSON
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (s *WarpService) DelWarpData() error {
	err := s.SettingService.SetWarp("")
	if err != nil {
		return err
	}
	return nil
}

func (s *WarpService) GetWarpConfig() (string, error) {
	var warpData map[string]string
	warp, err := s.SettingService.GetWarp()
	if err != nil {
		return "", err
	}
	err = json.Unmarshal([]byte(warp), &warpData)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("https://api.cloudflareclient.com/v0a2158/reg/%s", warpData["device_id"])

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+warpData["access_token"])

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	buffer := &bytes.Buffer{}
	_, err = buffer.ReadFrom(resp.Body)
	if err != nil {
		return "", err
	}

	return warpConfigWithPrivateKeyForBrowser(buffer.String(), warpData["private_key"])
}

func (s *WarpService) RegWarp(secretKey string, publicKey string) (string, error) {
	tos := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	hostName, _ := os.Hostname()
	data, err := json.Marshal(struct {
		Key   string `json:"key"`
		Tos   string `json:"tos"`
		Type  string `json:"type"`
		Model string `json:"model"`
		Name  string `json:"name"`
	}{Key: publicKey, Tos: tos, Type: "PC", Model: "vpn-ui", Name: hostName})
	if err != nil {
		return "", err
	}

	url := "https://api.cloudflareclient.com/v0a2158/reg"

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(data))
	if err != nil {
		return "", err
	}

	req.Header.Add("CF-Client-Version", "a-7.21-0721")
	req.Header.Add("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	buffer := &bytes.Buffer{}
	_, err = buffer.ReadFrom(resp.Body)
	if err != nil {
		return "", err
	}

	var rspData struct {
		ID      string `json:"id"`
		Token   string `json:"token"`
		Account struct {
			License string `json:"license"`
		} `json:"account"`
	}
	if err := json.Unmarshal(buffer.Bytes(), &rspData); err != nil {
		return "", err
	}
	if rspData.ID == "" || rspData.Token == "" || rspData.Account.License == "" {
		logger.Debug("Cloudflare WARP registration response omitted required fields.")
		return "", fmt.Errorf("Cloudflare WARP registration response is incomplete")
	}

	warpData, err := json.MarshalIndent(map[string]string{
		"access_token": rspData.Token,
		"device_id":    rspData.ID,
		"license_key":  rspData.Account.License,
		"private_key":  secretKey,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := s.SettingService.SetWarp(string(warpData)); err != nil {
		return "", err
	}

	browserData, err := warpDataForBrowser(string(warpData))
	if err != nil {
		return "", err
	}
	browserConfig, err := warpConfigForBrowser(buffer.String())
	if err != nil {
		return "", err
	}
	var result struct {
		Data   json.RawMessage `json:"data"`
		Config json.RawMessage `json:"config"`
	}
	result.Data = json.RawMessage(browserData)
	result.Config = json.RawMessage(browserConfig)
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (s *WarpService) SetWarpLicense(license string) (string, error) {
	var warpData map[string]string
	warp, err := s.SettingService.GetWarp()
	if err != nil {
		return "", err
	}
	err = json.Unmarshal([]byte(warp), &warpData)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("https://api.cloudflareclient.com/v0a2158/reg/%s/account", warpData["device_id"])
	data, err := json.Marshal(struct {
		License string `json:"license"`
	}{License: license})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("PUT", url, bytes.NewBuffer(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+warpData["access_token"])

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	buffer := &bytes.Buffer{}
	_, err = buffer.ReadFrom(resp.Body)
	if err != nil {
		return "", err
	}

	var response map[string]any
	err = json.Unmarshal(buffer.Bytes(), &response)
	if err != nil {
		return "", err
	}
	if response["success"] == false {
		errorArr, _ := response["errors"].([]any)
		errorObj := errorArr[0].(map[string]any)
		return "", common.NewError(errorObj["code"], errorObj["message"])
	}

	warpData["license_key"] = license
	newWarpData, err := json.MarshalIndent(warpData, "", "  ")
	if err != nil {
		return "", err
	}
	if err := s.SettingService.SetWarp(string(newWarpData)); err != nil {
		return "", err
	}
	return warpDataForBrowser(string(newWarpData))
}
