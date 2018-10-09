// Package echo implements a Service which echoes back !commands.
package echo

import (
	"mvdan.cc/xurls"

	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"fmt"
	"net/url"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	cache "github.com/patrickmn/go-cache"
	"github.com/Sirupsen/logrus"
	m "github.com/keighl/metabolize"

	"github.com/matrix-org/go-neb/types"
	"github.com/matrix-org/gomatrix"
)

var urlRegex = xurls.Strict()

// ServiceType of the music service
const ServiceType = "music"

type MetaData struct {
	Title       string  `meta:"og:title"`
	Description string  `meta:"og:description,description"`
	Type        string  `meta:"og:type"`
	URL         string  `meta:"og:url"`
	Image       url.URL `meta:"og:image"`
	VideoWidth  int64   `meta:"og:video:width"`
	VideoHeight int64   `meta:"og:video:height"`
	UploadedUrl string
}

// Service represents the Echo service. It has no Config fields.
type Service struct {
	types.DefaultService
}

// https://siongui.github.io/2016/05/10/go-get-html-title-via-net-html/
func isTitleElement(n *html.Node) bool {
	return n.Type == html.ElementNode && n.Data == "title"
}

func traverse(n *html.Node) (string, bool) {
	if isTitleElement(n) {
		return n.FirstChild.Data, true
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		result, ok := traverse(c)
		if ok {
			return result, ok
		}
	}

	return "", false
}

func getHtmlTitle(r io.Reader) (string, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", err
	}

	title, ok := traverse(doc)
	if ok != true {
		return "", errors.New("Could not parse title.")
	}

	return title, nil
}

func setUseragent(parsedUrl *url.URL, req *http.Request) {
	defaultUseragent := []string{
		"spotify.com", "youtube.com", "youtu.be",
	}

	for _, hostname := range defaultUseragent {
		if hostname == parsedUrl.Host || strings.HasSuffix(parsedUrl.Host, "." + hostname) {
			return
		}
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/60.0.3112.113 Safari/537.36")
	return
}

func fetchURL(client *gomatrix.Client, lock *sync.Mutex, urlCache *cache.Cache, fetchUrl string) (*MetaData, error) {
	lock.Lock()
	defer lock.Unlock()

	metadata := new(MetaData)

	cachedMetadata, found := urlCache.Get(fetchUrl)
	if found {
		return cachedMetadata.(*MetaData), nil
	}

	httpClient := &http.Client{
		Timeout: 3 * time.Second,
	}

	parsedUrl, err := url.Parse(fetchUrl)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("GET", fetchUrl, nil)
	if err != nil {
		return nil, err
	}

	setUseragent(parsedUrl, req)

	res, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != 200 {
		return nil, errors.New(fmt.Sprintf("Status code %d is not 200", res.StatusCode))
	}

	body, err := ioutil.ReadAll(io.LimitedReader{res.Body, 5*1024*1024})
	if err != nil {
		return nil, err
	}

	err = m.Metabolize(bytes.NewBuffer(body), metadata)
	if err != nil {
		return nil, err
	}

	if metadata.Image != (url.URL{}) {
		resUpload, err := client.UploadLink(metadata.Image.String())
		if err != nil {
			return nil, err
		}
		metadata.UploadedUrl = resUpload.ContentURI
	}

	if metadata.Title == "" {
		metadata.Title, err = getHtmlTitle(bytes.NewBuffer(body))
		if err != nil {
			return nil, err
		}
	}

	if metadata.URL == "" {
		metadata.URL = fetchUrl
	}

	urlCache.Set(fetchUrl, metadata, cache.DefaultExpiration)
	return metadata, nil
}

func (s *Service) Expansions(cli *gomatrix.Client) []types.Expansion {
	cache := cache.New(5*time.Minute, 10*time.Minute)
	lock := &sync.Mutex{}

	return []types.Expansion{
		types.Expansion{
			Regexp: urlRegex,
			Expand: func(roomID, userID string, urlGroups []string) interface{} {
				metadata, err := fetchURL(cli, lock, cache, urlGroups[0])
				if err != nil {
					logrus.Warning("Got error fetching URL:", err)
					return nil
				}

				if metadata.UploadedUrl == "" {
					logrus.Warning("No image!")
					return nil
				}

				return gomatrix.ImageMessage{
					MsgType: "m.image",
					Body: "",
					URL: metadata.UploadedUrl,
					Info: gomatrix.ImageInfo{
						Height: 120,
						Width: 120,
					},
				}
			},
		},
		types.Expansion{
			Regexp: urlRegex,
			Expand: func(roomID, userID string, urlGroups []string) interface{} {
				metadata, err := fetchURL(cli, lock, cache, urlGroups[0])
				if err != nil {
					logrus.Warning("Got error fetching URL:", err)
					return gomatrix.GetHTMLMessage(
						"m.notice",
						fmt.Sprintf("Error fetching: %s", err.Error()),
					)
				}

				if metadata.Title == "" {
					logrus.Warning("No description:", metadata)
					return nil
				}

				return gomatrix.GetHTMLMessage(
					"m.notice",
					fmt.Sprintf("<a href=\"%s\">%s</a><br>%s", metadata.URL, metadata.Title, metadata.Description),
				)
			},
		},
	}
}

func init() {
	types.RegisterService(func(serviceID, serviceUserID, webhookEndpointURL string) types.Service {
		return &Service{
			DefaultService: types.NewDefaultService(serviceID, serviceUserID, ServiceType),
		}
	})
}
