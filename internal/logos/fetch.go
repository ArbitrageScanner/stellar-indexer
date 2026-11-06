package logos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // decoders registered for image.Decode
	_ "image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// SafeClient is an HTTP client for fetching third-party URLs named by issuers:
// it dials only public unicast addresses (no loopback, private, link-local,
// CGNAT, multicast or unspecified ranges, checked after DNS resolution so a
// hostname cannot point it inward), ignores proxy settings, and follows at
// most three redirects, all https.
func SafeClient(timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second, Control: denyNonPublic}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns:          16,
		IdleConnTimeout:       30 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return errors.New("redirect to a non-https URL")
			}
			return nil
		},
	}
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// denyNonPublic is a net.Dialer Control hook: it runs on the resolved address.
func denyNonPublic(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if !publicIP(ip) {
		return fmt.Errorf("refusing to dial non-public address %s", ip)
	}
	return nil
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!ip.IsMulticast() && !ip.IsUnspecified() && !cgnat.Contains(ip)
}

// badImage marks a permanent refusal (wrong type, too large).
type badImage struct{ msg string }

func (e *badImage) Error() string { return e.msg }

// fetchImage downloads url (up to MaxDownloadBytes), checks its type by magic
// bytes (never the server's header), and re-encodes it with normalize, so
// what is stored is always a small PNG produced by this process.
func fetchImage(ctx context.Context, hc *http.Client, url string) ([]byte, string, error) {
	raw, err := download(ctx, hc, url)
	if err != nil {
		return nil, "", err
	}
	if Sniff(raw) == "" {
		return nil, "", &badImage{"image is not PNG, JPEG, GIF or WebP"}
	}
	out, err := normalize(raw)
	if err != nil {
		return nil, "", &badImage{err.Error()}
	}
	return out, "image/png", nil
}

func download(ctx context.Context, hc *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &badImage{"image URL: " + err.Error()}
	}
	req.Header.Set("Accept", "image/png, image/webp, image/jpeg, image/gif")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return nil, &badImage{fmt.Sprintf("image: HTTP %d", resp.StatusCode)}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxDownloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("image: %w", err)
	}
	if len(data) > MaxDownloadBytes {
		return nil, &badImage{fmt.Sprintf("image larger than %d bytes", MaxDownloadBytes)}
	}
	return data, nil
}

// MaxDownloadBytes caps a source image; MaxPixels caps its decoded size
// (checked from the header before decoding, against decompression bombs);
// LogoSize is the longest side of the stored PNG.
const (
	MaxDownloadBytes = 2 << 20
	MaxPixels        = 4096 * 4096
	LogoSize         = 128
)

// normalize decodes a raster image, scales it to fit LogoSize×LogoSize
// (never up) and re-encodes it as PNG. Only pixels survive: metadata,
// trailing bytes and anything smuggled in the container are dropped.
func normalize(raw []byte) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("image does not decode: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > MaxPixels {
		return nil, fmt.Errorf("image is %d×%d pixels", cfg.Width, cfg.Height)
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("image does not decode: %w", err)
	}
	w, h := cfg.Width, cfg.Height
	if w > LogoSize || h > LogoSize {
		if w >= h {
			w, h = LogoSize, max(1, h*LogoSize/cfg.Width)
		} else {
			w, h = max(1, w*LogoSize/cfg.Height), LogoSize
		}
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, dst); err != nil {
		return nil, err
	}
	if buf.Len() > MaxImageBytes {
		return nil, fmt.Errorf("re-encoded image is %d bytes", buf.Len())
	}
	return buf.Bytes(), nil
}

// Sniff returns the raster type of b by its signature, or "" for anything
// else (SVG and HTML included).
func Sniff(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return "image/gif"
	case len(b) >= 12 && bytes.Equal(b[0:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return "image/webp"
	}
	return ""
}
