package core

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type catpawYiHTTPTransport struct {
	base, api http.RoundTripper
	origin    string
}

func (transport *catpawYiHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme == "https" && providerMediaOrigin(request.URL) == transport.origin {
		return transport.api.RoundTrip(request)
	}
	return transport.base.RoundTrip(request)
}

func catpawYiVerifyConnection(state tls.ConnectionState, host, pin string, roots *x509.CertPool, now time.Time) error {
	if len(state.PeerCertificates) == 0 {
		return errors.New("壹影视接口未提供 TLS 证书")
	}
	leaf := state.PeerCertificates[0]
	options := x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: x509.NewCertPool(), CurrentTime: now}
	for _, certificate := range state.PeerCertificates[1:] {
		options.Intermediates.AddCert(certificate)
	}
	_, err := leaf.Verify(options)
	if err == nil {
		return nil
	}
	var invalid x509.CertificateInvalidError
	digest := sha256.Sum256(leaf.Raw)
	if !errors.As(err, &invalid) || invalid.Reason != x509.Expired || invalid.Cert != leaf || !now.After(leaf.NotAfter) ||
		hex.EncodeToString(digest[:]) != pin {
		return err
	}
	for _, certificate := range state.PeerCertificates[1:] {
		if now.Before(certificate.NotBefore) || now.After(certificate.NotAfter) {
			return err
		}
	}
	options.CurrentTime = leaf.NotAfter.Add(-time.Second)
	_, err = leaf.Verify(options)
	return err
}

func (c *attachedClient) cpYiTransport() http.RoundTripper {
	base := c.d.client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	original := base
	if browser, ok := base.(*huangguoBrowserTransport); ok {
		original = browser.base
	}
	if cdn, ok := original.(*cdnTransport); ok {
		original = cdn.base
	}
	transport, ok := original.(*http.Transport)
	pin := c.access.Settings["apiExpiredCertificateSHA256"]
	digest, err := hex.DecodeString(pin)
	address, addressErr := url.Parse(c.catpaw.base)
	if !ok || err != nil || len(digest) != sha256.Size || addressErr != nil || address.Scheme != "https" || address.Hostname() == "" {
		return base
	}
	api := transport.Clone()
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if api.TLSClientConfig != nil {
		config = api.TLSClientConfig.Clone()
	}
	config.ServerName = address.Hostname()
	config.InsecureSkipVerify = true
	config.VerifyPeerCertificate = nil
	config.VerifyConnection = func(state tls.ConnectionState) error {
		return catpawYiVerifyConnection(state, address.Hostname(), pin, config.RootCAs, time.Now())
	}
	api.TLSClientConfig = config
	return &catpawYiHTTPTransport{base: base, api: api, origin: providerMediaOrigin(address)}
}

func (c *attachedClient) cpYiRequest(ctx context.Context, method, path string, params url.Values) (any, error) {
	for attempt := 0; attempt < 2; attempt++ {
		params.Set("timestamp", strconv.FormatInt(time.Now().Unix(), 10))
		headers := map[string]string{
			"APP-ID": c.cpValue("appID"), "Authorization": "",
			"X-HASH-Data": attachedSHA(catpawSortedParams(params) + "&token=" + c.cpValue("token")),
		}
		target, body, contentType := path, "", ""
		if method == http.MethodGet {
			target = attachedPath(path, params)
		} else {
			body, contentType = params.Encode(), "application/x-www-form-urlencoded"
		}
		raw, status, err := c.cpRawResponse(ctx, method, target, contentType, body, headers)
		value, decodeErr := catpawParseJSON(raw)
		refresh := status == http.StatusBadRequest || err == nil && strings.TrimSpace(raw) == "" ||
			decodeErr == nil && mapString(attachedObject(value), "code") == "400"
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if refresh && attempt == 0 {
			if err = c.cpYiToken(ctx); err != nil {
				c.catpaw.ready = time.Time{}
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if refresh {
			c.catpaw.ready = time.Time{}
			return nil, errors.New("壹影视签名会话未恢复，请稍后重试")
		}
		return c.cpDecode(raw)
	}
	return nil, errors.New("壹影视接口请求失败")
}

func (c *attachedClient) cpYiMedia(ctx context.Context, address string, headers map[string]string) (providerMedia, error) {
	media, err := catpawURLMedia(address, headers)
	if err != nil || maccmsDirectMediaURL(address) != "" {
		return media, err
	}
	media.Playlist, media.URL, err = c.d.fetchMediaPlaylist(providerMediaContext(ctx, media.credentials), address, media.Referer)
	if err != nil {
		return providerMedia{}, err
	}
	media.Duration = m3u8Duration(media.Playlist)
	return media, nil
}
