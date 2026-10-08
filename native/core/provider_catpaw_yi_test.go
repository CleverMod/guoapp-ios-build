package core

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCatpawYiExpiredCertificateRequiresExactPinHostAndTrustedChain(t *testing.T) {
	now := time.Now()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: now.Add(-72 * time.Hour), NotAfter: now.Add(72 * time.Hour)}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root, err = x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"yi.test"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		NotBefore: now.Add(-48 * time.Hour), NotAfter: now.Add(-time.Hour)}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err = x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(leaf.Raw)
	pin := hex.EncodeToString(digest[:])
	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf, root}}
	for _, fixture := range []struct {
		name, host, pin string
		roots           *x509.CertPool
		wantError       bool
	}{
		{"pinned expired leaf", "yi.test", pin, roots, false},
		{"missing pin", "yi.test", "", roots, true},
		{"different certificate", "yi.test", strings.Repeat("0", 64), roots, true},
		{"different host", "other.test", pin, roots, true},
		{"untrusted chain", "yi.test", pin, x509.NewCertPool(), true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			err := catpawYiVerifyConnection(state, fixture.host, fixture.pin, fixture.roots, now)
			if (err != nil) != fixture.wantError {
				t.Fatal("certificate policy mismatch", err)
			}
		})
	}
	if err := catpawYiVerifyConnection(state, "yi.test", "", roots, leaf.NotAfter.Add(-time.Minute)); err != nil {
		t.Fatal("valid certificate unexpectedly required an exception", err)
	}
	if err := catpawYiVerifyConnection(state, "yi.test", pin, roots, root.NotAfter.Add(time.Hour)); err == nil {
		t.Fatal("accepted an expired certificate chain")
	}
	apiCalls, baseCalls := 0, 0
	transport := &catpawYiHTTPTransport{origin: "https://yi.test",
		api: sourceFixtureTransport(func(r *http.Request) (*http.Response, error) {
			apiCalls++
			return sourceFixtureResponse(r, 200, ""), nil
		}),
		base: sourceFixtureTransport(func(r *http.Request) (*http.Response, error) {
			baseCalls++
			return sourceFixtureResponse(r, 200, ""), nil
		})}
	for _, address := range []string{"https://yi.test/api", "https://media.test/movie", "https://yi.test:8443/api", "http://yi.test/api"} {
		r, _ := http.NewRequest(http.MethodGet, address, nil)
		response, err := transport.RoundTrip(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
	}
	if apiCalls != 1 || baseCalls != 3 {
		t.Fatal("TLS exception applied outside the configured API origin")
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	c := &attachedClient{d: &Downloader{client: &http.Client{Transport: &huangguoBrowserTransport{base: newCDNTransport(base, nil)}}},
		access: attachedAccess{Settings: map[string]string{"apiExpiredCertificateSHA256": pin}}, catpaw: &catpawState{base: "https://yi.test"}}
	wrapped, ok := c.cpYiTransport().(*catpawYiHTTPTransport)
	if !ok || wrapped.api.(*http.Transport).TLSClientConfig.VerifyConnection == nil || base.TLSClientConfig != nil {
		t.Fatal("production transport did not isolate the certificate verification policy")
	}
}

func TestCatpawYiRefreshesHTTPSignatureFailureAndEmptyResponseOnce(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public})
	padding := make([]byte, key.Size())
	padding[1] = 1
	separator := len(padding) - len("fresh-token") - 1
	for i := 2; i < separator; i++ {
		padding[i] = 255
	}
	copy(padding[separator+1:], "fresh-token")
	signed := new(big.Int).Exp(new(big.Int).SetBytes(padding), key.D, key.N).FillBytes(make([]byte, key.Size()))
	tokenResponse := attachedJSON(map[string]any{"code": "200", "data": base64.StdEncoding.EncodeToString(signed)})
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, failure := range []struct {
			name   string
			status int
			body   string
		}{
			{"HTTP 400", 400, ""}, {"empty response", 200, ""}, {"JSON 400", 200, `{"code":"400"}`},
		} {
			t.Run(method+"/"+failure.name, func(t *testing.T) {
				apiCalls, refreshes := 0, 0
				d := sourceFixtureDownloader(t, func(r *http.Request) (*http.Response, error) {
					if err := r.ParseForm(); err != nil {
						t.Fatal(err)
					}
					if r.URL.Path == "/vod-app/index/getGenerateKey" {
						refreshes++
						if r.Form.Get("appID") != "0123456789abcdef" || r.Header.Get("X-Auth-Flow") != "1" || r.Header.Get("X-HASH-Data") != "" {
							t.Fatal("invalid token refresh")
						}
						return sourceFixtureResponse(r, 200, tokenResponse), nil
					}
					apiCalls++
					if r.Form.Get("sourceCode") != "line-two" || r.Form.Get("urlEncode") != "opaque+token&part=2" || r.Form.Get("timestamp") == "" {
						t.Fatal("retry changed the selected line or encoded episode")
					}
					token := "stale-token"
					if apiCalls == 2 {
						token = "fresh-token"
					}
					if r.Header.Get("X-HASH-Data") != attachedSHA(catpawSortedParams(r.Form)+"&token="+token) {
						t.Fatal("request was not re-signed with the current token")
					}
					if apiCalls == 1 {
						return sourceFixtureResponse(r, failure.status, failure.body), nil
					}
					return sourceFixtureResponse(r, 200, `{"code":"200","data":{"url":"https://media.test/movie.mp4"}}`), nil
				})
				c, err := d.attachedClient("catpaw_yiys")
				if err != nil {
					t.Fatal(err)
				}
				c.catpaw = &catpawState{script: "壹影视.py", base: "https://yi.test", headers: map[string]string{},
					constants: map[string]string{"const3": string(encoded)}, values: map[string]string{"appID": "0123456789abcdef", "token": "stale-token"}}
				data, err := c.cpRequest(context.Background(), method, "/vod-app/vod/playUrl", attachedParams("sourceCode", "line-two", "urlEncode", "opaque+token&part=2"))
				if err != nil || catpawResponseURL(data) != "https://media.test/movie.mp4" || apiCalls != 2 || refreshes != 1 {
					t.Fatal("signature recovery failed or retried more than once", err)
				}
			})
		}
	}
}

func TestCatpawYiNoExtensionHLSKeepsPlayerHeadersAndRejectsErrorPages(t *testing.T) {
	for _, valid := range []bool{true, false} {
		d := sourceFixtureDownloader(t, func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/vod-app/vod/playUrl":
				if err := r.ParseForm(); err != nil || r.Form.Get("sourceCode") != "main" || r.Form.Get("urlEncode") != "episode-token" {
					t.Fatal("selected episode was changed", err)
				}
				return sourceFixtureResponse(r, 200, `{"code":"200","data":{"url":"https://media.test/nby/m3u8/getM3u8?url=a%2Bb"}}`), nil
			case "/nby/m3u8/getM3u8":
				if r.Header.Get("User-Agent") != "Android/OkHttp" || r.Header.Get("X-HASH-Data") != "" || r.Header.Get("APP-ID") != "" || r.URL.Query().Get("url") != "a+b" {
					t.Fatal("media headers, signature boundary or encoded URL changed")
				}
				body := "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\nsegment.ts\n#EXT-X-ENDLIST\n"
				if !valid {
					body = "<pre>expired media token</pre>"
				}
				response := sourceFixtureResponse(r, 200, body)
				response.Header.Set("Content-Type", "application/octet-stream")
				return response, nil
			default:
				t.Fatal("unexpected global parser or image request", r.URL.Path)
				return nil, nil
			}
		})
		c, err := d.attachedClient("catpaw_yiys")
		if err != nil {
			t.Fatal(err)
		}
		c.catpaw = &catpawState{script: "壹影视.py", base: "https://yi.test", headers: map[string]string{"User-Agent": "Android/OkHttp"},
			values: map[string]string{"appID": "0123456789abcdef", "token": "fixture-token"}}
		media, err := c.catpawPlayEpisode(context.Background(), "7", "main", catpawEpisode{URL: "episode-token"})
		if valid && (err != nil || media.Playlist == "" || media.Duration != 6*time.Second) {
			t.Fatal("HLS endpoint without an extension was rejected", err)
		}
		if !valid && err == nil {
			t.Fatal("error page was accepted as media")
		}
	}
}
