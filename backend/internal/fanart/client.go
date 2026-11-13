package fanart

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"qmediasync/internal/helpers"

	"resty.dev/v3"
)

const (
	FANART_API_URL = "https://webservice.fanart.tv/v3/"
)

// Client represents a fanart.tv API client
type Client struct {
	apiKey      string
	baseURL     string
	restyClient *resty.Client
}

// NewClient creates a new fanart.tv API client
func NewClient(apiKey, proxyURL string) (*Client, error) {
	transport, err := helpers.GetProxyTransport(proxyURL)
	if err != nil {
		return nil, err
	}
	client := resty.NewWithClient(&http.Client{Transport: transport})

	// Set default timeout
	client.SetTimeout(30 * time.Second)

	// Set default headers
	client.SetHeader("Accept", "application/json")
	client.SetHeader("Content-Type", "application/json")

	// Set User-Agent
	client.SetHeader("User-Agent", "qmediasync-go/1.0")
	client.SetBaseURL(FANART_API_URL)

	return &Client{
		apiKey:      apiKey,
		baseURL:     FANART_API_URL,
		restyClient: client,
	}, nil
}

// Close 释放本客户端持有的私有连接资源。
func (c *Client) Close() error { return helpers.CloseRestyClient(c.restyClient) }

// SetBaseURL sets the base URL for the API
func (c *Client) SetBaseURL(baseURL string) {
	c.baseURL = baseURL
	c.restyClient.SetBaseURL(baseURL)
}

// SetTimeout sets the request timeout
func (c *Client) SetTimeout(timeout time.Duration) {
	c.restyClient.SetTimeout(timeout)
}

// doRequest performs the actual HTTP request
func (c *Client) doRequest(request *resty.Request, method, url string) (*resty.Response, error) {
	// Add API key as query parameter
	request.SetQueryParam("api_key", c.apiKey)

	var resp *resty.Response
	var err error

	switch method {
	case http.MethodGet:
		resp, err = request.Get(url)
	default:
		return nil, fmt.Errorf("unsupported HTTP method: %s", method)
	}

	if err != nil {
		return nil, err
	}

	// Check for HTTP errors
	if resp.StatusCode() >= 400 {
		return nil, fmt.Errorf("HTTP error %d: %s", resp.StatusCode(), resp.String())
	}

	return resp, nil
}

// MovieImagesResponse represents the response structure for movie images
type MovieImagesResponse struct {
	Name            string `json:"name"`
	TmdbID          string `json:"tmdb_id"`
	ImdbID          string `json:"imdb_id"`
	HDMovieClearArt []struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Lang  string `json:"lang"`
		Likes string `json:"likes"`
	} `json:"hdmovieclearart"`
	HDMovieLogo []struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Lang  string `json:"lang"`
		Likes string `json:"likes"`
	} `json:"hdmovielogo"`
	MovieBackground []struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Lang  string `json:"lang"`
		Likes string `json:"likes"`
	} `json:"moviebackground"`
	MovieBanner []struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Lang  string `json:"lang"`
		Likes string `json:"likes"`
	} `json:"moviebanner"`
	MovieDisc []struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Lang  string `json:"lang"`
		Likes string `json:"likes"`
	} `json:"moviedisc"`
	MovieThumb []struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Lang  string `json:"lang"`
		Likes string `json:"likes"`
	} `json:"moviethumb"`
	MoviePoster []struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Lang  string `json:"lang"`
		Likes string `json:"likes"`
	} `json:"movieposter"`
	ClearLogo []struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Lang  string `json:"lang"`
		Likes string `json:"likes"`
	} `json:"clearlogo"`
	MovieArt []struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Lang  string `json:"lang"`
		Likes string `json:"likes"`
	} `json:"movieart"`
	MovieSquare []struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Lang  string `json:"lang"`
		Likes string `json:"likes"`
	} `json:"moviesquare"`
	Movie4kBackground []struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Lang  string `json:"lang"`
		Likes string `json:"likes"`
	} `json:"movie4kbackground"`
}

// GetMovieImages retrieves images for a specific movie by its TMDB ID
func (c *Client) GetMovieImages(ctx context.Context, movieID int64) (*MovieImagesResponse, error) {
	url := fmt.Sprintf("movies/%d", movieID)

	request := c.restyClient.R().SetContext(ctx)
	request.SetResult(&MovieImagesResponse{})

	resp, err := c.doRequest(request, http.MethodGet, url)
	if err != nil {
		return nil, err
	}

	return resp.Result().(*MovieImagesResponse), nil
}

// ShowImagesResponse represents the response structure for TV show images
type ShowImagesResponse struct {
	Name       string `json:"name"`
	ThetvdbID  string `json:"thetvdb_id"`
	HDClearArt []struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		Lang   string `json:"lang"`
		Likes  string `json:"likes"`
		Season string `json:"season,omitempty"`
	} `json:"hdclearart"`
	ClearLogo []struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		Lang   string `json:"lang"`
		Likes  string `json:"likes"`
		Season string `json:"season,omitempty"`
	} `json:"clearlogo"`
	ShowBackground []struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		Lang   string `json:"lang"`
		Likes  string `json:"likes"`
		Season string `json:"season,omitempty"`
	} `json:"showbackground"`
	SeasonPoster []struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		Lang   string `json:"lang"`
		Likes  string `json:"likes"`
		Season string `json:"season"`
	} `json:"seasonposter"`
	ShowBanner []struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		Lang   string `json:"lang"`
		Likes  string `json:"likes"`
		Season string `json:"season,omitempty"`
	} `json:"showbanner"`
	ShowThumb []struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		Lang   string `json:"lang"`
		Likes  string `json:"likes"`
		Season string `json:"season,omitempty"`
	} `json:"showthumb"`
	SeasonThumb []struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		Lang   string `json:"lang"`
		Likes  string `json:"likes"`
		Season string `json:"season"`
	} `json:"seasonthumb"`
	CharacterArt []struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Lang  string `json:"lang"`
		Likes string `json:"likes"`
	} `json:"characterart"`
	TVPoster []struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		Lang   string `json:"lang"`
		Likes  string `json:"likes"`
		Season string `json:"season,omitempty"`
	} `json:"tvposter"`
}

// GetShowImages retrieves images for a specific TV show by its TVDB ID
func (c *Client) GetShowImages(ctx context.Context, showID string) (*ShowImagesResponse, error) {
	url := fmt.Sprintf("tv/%s", showID)

	request := c.restyClient.R().SetContext(ctx)
	request.SetResult(&ShowImagesResponse{})

	resp, err := c.doRequest(request, http.MethodGet, url)
	if err != nil {
		return nil, err
	}

	return resp.Result().(*ShowImagesResponse), nil
}
