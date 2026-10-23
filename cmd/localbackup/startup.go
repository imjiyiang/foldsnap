package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"localbackup/internal/backup"
)

type serviceEndpoint struct {
	Address string `json:"address"`
}

func listenLocal(address string, fallback bool) (net.Listener, error) {
	l, err := net.Listen("tcp", address)
	if err != nil && fallback && errors.Is(err, syscall.EADDRINUSE) {
		host, _, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			return nil, splitErr
		}
		return net.Listen("tcp", net.JoinHostPort(host, "0"))
	}
	return l, err
}

func writeEndpoint(data, address string) error {
	b, err := json.Marshal(serviceEndpoint{address})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(data, ".endpoint-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(data, "service-endpoint.json"))
}

// A held data-directory lock alone does not identify the service on a port.
// Authenticate against the endpoint published by that exact directory instead.
func existingServiceURL(data string, wait time.Duration) (string, error) {
	deadline := time.Now().Add(wait)
	client := &http.Client{Timeout: 500 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	for {
		url, token, err := endpointURL(data)
		if err == nil {
			req, _ := http.NewRequest("GET", url+"api/v1/system/status", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			res, requestErr := client.Do(req)
			if requestErr == nil {
				var status struct {
					Service string `json:"service"`
				}
				decodeErr := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 64<<10)).Decode(&status)
				res.Body.Close()
				if res.StatusCode == 200 && decodeErr == nil && status.Service == "ready" {
					return url + "#token=" + token, nil
				}
			}
		}
		if !time.Now().Before(deadline) {
			return "", fmt.Errorf("无法验证现有服务；请检查该数据目录的 service-endpoint.json 或重新启动服务")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func endpointURL(data string) (string, string, error) {
	b, err := backup.ReadMetadataFile(filepath.Join(data, "service-endpoint.json"), 4096)
	if err != nil {
		return "", "", err
	}
	var endpoint serviceEndpoint
	if err = json.Unmarshal(b, &endpoint); err != nil {
		return "", "", err
	}
	host, port, err := net.SplitHostPort(endpoint.Address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || port == "0" {
		return "", "", fmt.Errorf("无效服务地址")
	}
	token, err := backup.ReadMetadataFile(filepath.Join(data, "access.token"), 4096)
	if err != nil {
		return "", "", err
	}
	t := strings.TrimSpace(string(token))
	if len(t) < 24 || strings.ContainsAny(t, "\r\n #?&") {
		return "", "", fmt.Errorf("无效访问令牌")
	}
	return "http://" + endpoint.Address + "/", t, nil
}
