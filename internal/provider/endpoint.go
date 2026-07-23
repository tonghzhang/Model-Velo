package provider

import (
	"errors"
	"net/url"
	"strings"
)

// parseBaseURL 在配置边界拒绝凭据、查询参数和片段，避免密钥泄漏或端点歧义。
func parseBaseURL(rawBaseURL string) (*url.URL, error) {
	baseURL := strings.TrimSpace(rawBaseURL)
	if baseURL == "" {
		return nil, errors.New("upstream base URL is required")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, errors.New("upstream base URL is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("upstream base URL must use http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("upstream base URL must include a host")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("upstream base URL must not contain credentials, query, or fragment")
	}
	return parsed, nil
}

// fixedEndpoint 追加固定 API 路径，并允许 base_url 已包含该路径的前缀。
func fixedEndpoint(rawBaseURL, suffix string) (string, error) {
	parsed, err := parseBaseURL(rawBaseURL)
	if err != nil {
		return "", err
	}

	basePath := strings.TrimRight(parsed.Path, "/")
	suffix = strings.Trim(suffix, "/")
	fullSuffix := "/" + suffix
	if !strings.HasSuffix(basePath, fullSuffix) {
		firstSegment, remaining, _ := strings.Cut(suffix, "/")
		if remaining != "" && strings.HasSuffix(basePath, "/"+firstSegment) {
			basePath += "/" + remaining
		} else {
			basePath += fullSuffix
		}
	}
	parsed.Path = basePath
	parsed.RawPath = ""
	return parsed.String(), nil
}

// modelEndpoint 将模型名安全地放入 URL 路径；包含路径控制字符的模型名会被拒绝。
func modelEndpoint(rawBaseURL, prefix, model, action string) (string, error) {
	parsed, err := parseBaseURL(rawBaseURL)
	if err != nil {
		return "", err
	}
	model = strings.TrimSpace(strings.TrimPrefix(model, prefix+"/"))
	if model == "" || strings.ContainsAny(model, "/?#") {
		return "", ErrInvalidRequest
	}

	basePath := strings.TrimRight(parsed.Path, "/")
	parsed.Path = basePath + "/" + prefix + "/" + url.PathEscape(model) + action
	parsed.RawPath = ""
	return parsed.String(), nil
}
