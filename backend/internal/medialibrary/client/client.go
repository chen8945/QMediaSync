package embyclientrestgo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Product 是服务器明确返回的产品；未知产品沿用 Emby 协议。
type Product string

const (
	ProductUnknown  Product = ""
	ProductEmby     Product = "emby"
	ProductJellyfin Product = "jellyfin"
)

// ConnectionInfo 区分产品确认结果和不阻断业务的品牌探测诊断。
type ConnectionInfo struct {
	Product    Product
	Diagnostic error
}

// Client 持有单个连接的传输、身份及产品结果，不跨连接共享凭据。
type Client struct {
	embyURL         string
	apiKey          string
	expectedProduct Product
	httpClient      *http.Client
	infoMu          sync.Mutex
	serverInfo      *serverInfo
	infoLoading     *infoCall
	product         Product
	productLoading  *productCall
}

type serverInfo struct {
	ID          string `json:"Id"`
	ProductName string `json:"ProductName"`
}

type infoCall struct {
	done chan struct{}
	info serverInfo
	err  error
}

type productCall struct {
	done chan struct{}
	info ConnectionInfo
	err  error
}

// NewClient 创建不限定预期产品的媒体服务器客户端。
func NewClient(serverURL, apiKey string) *Client {
	return NewClientForProduct(serverURL, apiKey, ProductUnknown)
}

// NewClientForProduct 将固定业务入口的预期产品绑定到连接检查。
func NewClientForProduct(serverURL, apiKey string, expected Product, transport ...http.RoundTripper) *Client {
	client := &Client{embyURL: serverURL, apiKey: apiKey, expectedProduct: expected, httpClient: &http.Client{Timeout: 30 * time.Second}}
	if len(transport) != 0 {
		client.httpClient.Transport = transport[0]
	}
	return client
}

func identifyProduct(name string) Product {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "emby", "emby server":
		return ProductEmby
	case "jellyfin", "jellyfin server":
		return ProductJellyfin
	default:
		return ProductUnknown
	}
}

// CheckConnection 复用明确品牌结果，仅在产品错配或调用取消时阻止业务。
func (c *Client) CheckConnection(ctx context.Context) (ConnectionInfo, error) {
	if err := ctx.Err(); err != nil {
		return ConnectionInfo{}, err
	}
	if _, err := c.checkExpected(ConnectionInfo{}); err != nil {
		return ConnectionInfo{}, err
	}
	c.infoMu.Lock()
	product := c.product
	if product != ProductUnknown {
		c.infoMu.Unlock()
		return c.checkExpected(ConnectionInfo{Product: product})
	}
	if call := c.productLoading; call != nil {
		c.infoMu.Unlock()
		select {
		case <-ctx.Done():
			return ConnectionInfo{}, ctx.Err()
		case <-call.done:
			if err := ctx.Err(); err != nil {
				return ConnectionInfo{}, err
			}
			if errors.Is(call.err, context.Canceled) || errors.Is(call.err, context.DeadlineExceeded) {
				return c.CheckConnection(ctx)
			}
			if call.err != nil {
				return call.info, call.err
			}
			return c.checkExpected(call.info)
		}
	}
	call := &productCall{done: make(chan struct{})}
	c.productLoading = call
	c.infoMu.Unlock()
	call.info = c.probeProduct(ctx)
	if ctx.Err() != nil {
		call.err = ctx.Err()
	}
	c.infoMu.Lock()
	// Public 可能在 Ping 期间已取得明确产品。
	if c.product != ProductUnknown {
		call.info = ConnectionInfo{Product: c.product}
	} else if call.info.Product != ProductUnknown {
		c.product = call.info.Product
	}
	c.productLoading = nil
	close(call.done)
	c.infoMu.Unlock()
	if call.err != nil {
		return call.info, call.err
	}
	return c.checkExpected(call.info)
}

func (c *Client) checkExpected(info ConnectionInfo) (ConnectionInfo, error) {
	if c.expectedProduct != ProductUnknown && c.expectedProduct != ProductEmby && c.expectedProduct != ProductJellyfin {
		return info, errors.New("无效的媒体服务器产品")
	}
	if info.Product != ProductUnknown && c.expectedProduct != ProductUnknown && info.Product != c.expectedProduct {
		productNames := map[Product]string{ProductEmby: "Emby", ProductJellyfin: "Jellyfin"}
		return info, fmt.Errorf("媒体服务器产品为 %s，与配置的 %s 不符，请调整配置", productNames[info.Product], productNames[c.expectedProduct])
	}
	return info, nil
}

func (c *Client) probeProduct(ctx context.Context) ConnectionInfo {
	for _, native := range []bool{true, false} {
		req, err := c.newAPIRequest(ctx, http.MethodGet, "/System/Ping", nil, native)
		if err != nil {
			return ConnectionInfo{Diagnostic: err}
		}
		resp, err := c.do(req)
		if err != nil {
			return ConnectionInfo{Diagnostic: err}
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound && native {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return ConnectionInfo{Diagnostic: fmt.Errorf("媒体服务器产品探测状态码：%d", resp.StatusCode)}
		}
		if readErr != nil || len(body) > 4096 {
			return ConnectionInfo{Diagnostic: errors.New("读取媒体服务器产品响应失败")}
		}
		name := strings.TrimSpace(string(body))
		if strings.HasPrefix(name, "\"") {
			if err := json.Unmarshal(body, &name); err != nil {
				return ConnectionInfo{Diagnostic: errors.New("解析媒体服务器产品响应失败")}
			}
		}
		product := identifyProduct(name)
		if product == ProductUnknown {
			return ConnectionInfo{Diagnostic: errors.New("媒体服务器未返回可确认的产品标识")}
		}
		return ConnectionInfo{Product: product}
	}
	return ConnectionInfo{}
}

// GetServerID 读取真实实例身份；产品探测失败不能代替身份或权限校验。
func (c *Client) GetServerID(ctx context.Context) (string, error) {
	if _, err := c.checkExpected(ConnectionInfo{}); err != nil {
		return "", err
	}
	info, err := c.getServerInfo(ctx)
	if err != nil {
		return "", err
	}
	if _, err := c.CheckConnection(ctx); err != nil {
		return "", err
	}
	return info.ID, nil
}

func (c *Client) getServerInfo(ctx context.Context) (serverInfo, error) {
	if err := ctx.Err(); err != nil {
		return serverInfo{}, err
	}
	c.infoMu.Lock()
	if c.serverInfo != nil {
		info := *c.serverInfo
		c.infoMu.Unlock()
		return info, nil
	}
	if call := c.infoLoading; call != nil {
		c.infoMu.Unlock()
		select {
		case <-ctx.Done():
			return serverInfo{}, ctx.Err()
		case <-call.done:
			if err := ctx.Err(); err != nil {
				return serverInfo{}, err
			}
			if errors.Is(call.err, context.Canceled) || errors.Is(call.err, context.DeadlineExceeded) {
				return c.getServerInfo(ctx)
			}
			return call.info, call.err
		}
	}
	call := &infoCall{done: make(chan struct{})}
	c.infoLoading = call
	c.infoMu.Unlock()
	status, err := c.getJSON(ctx, "/System/Info/Public", nil, &call.info, true)
	if status == http.StatusNotFound {
		_, err = c.getJSON(ctx, "/System/Info/Public", nil, &call.info, false)
	}
	if err == nil && strings.TrimSpace(call.info.ID) == "" {
		err = errors.New("媒体服务器未返回服务器 ID")
	}
	call.err = err
	c.infoMu.Lock()
	if err == nil {
		info := call.info
		c.serverInfo = &info
		if product := identifyProduct(info.ProductName); product != ProductUnknown {
			c.product = product
		}
	}
	c.infoLoading = nil
	close(call.done)
	c.infoMu.Unlock()
	return call.info, call.err
}

func (c *Client) isJellyfin() bool {
	c.infoMu.Lock()
	defer c.infoMu.Unlock()
	return c.product == ProductJellyfin
}

func (c *Client) newAPIRequest(ctx context.Context, method, path string, params url.Values, jellyfin bool) (*http.Request, error) {
	if jellyfin {
		return c.jellyfinRequest(ctx, method, path, params)
	}
	return c.embyRequest(ctx, method, path, params)
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		return nil, errors.New("媒体服务器请求失败")
	}
	return resp, nil
}

func decodeJSON(reader io.Reader, result any) error {
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(result); err != nil {
		return fmt.Errorf("解析媒体服务器响应失败：%w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("媒体服务器响应含有多余内容")
	}
	return nil
}

func (c *Client) getSnapshotJSON(ctx context.Context, path string, params url.Values, result any) error {
	// 公开操作先检查连接，内部分页和补查复用结果，不逐次重试失败的品牌探测。
	_, err := c.getJSON(ctx, path, params, result, c.isJellyfin())
	return err
}

func (c *Client) getJSON(ctx context.Context, path string, params url.Values, result any, jellyfin bool) (int, error) {
	req, err := c.newAPIRequest(ctx, http.MethodGet, path, params, jellyfin)
	if err != nil {
		return 0, err
	}
	resp, err := c.do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("媒体服务器请求状态码：%d", resp.StatusCode)
	}
	return resp.StatusCode, decodeJSON(resp.Body, result)
}
