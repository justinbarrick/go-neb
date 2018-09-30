// Package echo implements a Service which echoes back !commands.
package echo

import (
	"mvdan.cc/xurls"

	"errors"
	"sync"
	"fmt"
	"net/url"
	"net/http"
	"time"

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
	URL         url.URL `meta:"og:url"`
	Image       url.URL `meta:"og:image"`
	VideoWidth  int64   `meta:"og:video:width"`
	VideoHeight int64   `meta:"og:video:height"`
	UploadedUrl string
}

// Service represents the Echo service. It has no Config fields.
type Service struct {
	types.DefaultService
}

func fetchURL(client *gomatrix.Client, lock *sync.Mutex, urlCache *cache.Cache, fetchUrl string) (*MetaData, error) {
	lock.Lock()
	defer lock.Unlock()

	metadata := new(MetaData)

	cachedMetadata, found := urlCache.Get(fetchUrl)
	if found {
		return cachedMetadata.(*MetaData), nil
	}

	httpClient := &http.Client{}

	req, err := http.NewRequest("GET", fetchUrl, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:61.0) Gecko/20100101 Firefox/61.0")

	res, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != 200 {
		return nil, errors.New(fmt.Sprintf("Status code %d is not 200", res.StatusCode))
	}

	err = m.Metabolize(res.Body, metadata)
	if err != nil {
		return nil, err
	}

	if metadata.Image != (url.URL{}) {
		resUpload, err := client.UploadLink(metadata.Image.String())
		if err != nil {
			return metadata, err
		}
		metadata.UploadedUrl = resUpload.ContentURI
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

				if metadata.Description == "" {
					logrus.Warning("No description!")
					return nil
				}

				return gomatrix.GetHTMLMessage(
					"m.notice",
					fmt.Sprintf("<a href=\"%s\">%s</a><br>%s", metadata.URL.String(), metadata.Title, metadata.Description),
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
