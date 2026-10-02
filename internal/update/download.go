package update

import (
	"archive/zip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
)

// Release files, as published by .github/workflows/release.yml.
const (
	SumsName = "SHA256SUMS.txt"
	SigName  = SumsName + ".sig"
)

// ZipName is the portable package's file name for version v ("0.2.0").
func ZipName(v string) string { return "tunneltab-" + v + ".zip" }

// DefaultDownloadPrefix is where TunnelTab's release files live on GitHub.
const DefaultDownloadPrefix = "https://github.com/Aerobit/TunnelTab/releases/download/"

// downloadPrefix is where release files may come from: GitHub for the real
// API, or the same server as a test API given with --update-url.
func downloadPrefix(apiURL string) string {
	if apiURL == DefaultURL {
		return DefaultDownloadPrefix
	}
	u, err := url.Parse(apiURL)
	if err != nil || u.Host == "" {
		return DefaultDownloadPrefix
	}
	return u.Scheme + "://" + u.Host + "/"
}

// PackageFiles are the files in the portable folder that an update
// replaces. The data folder is never touched (the zip doesn't contain one).
var PackageFiles = []string{"tunneltab.exe", "tunneltab-linux-amd64", "README.txt", "LICENSE.txt", "THIRD_PARTY_NOTICES.txt"}

// ExeName is the package file that runs on this system, or "" if releases
// have none (e.g. macOS or ARM).
func ExeName() string {
	switch {
	case runtime.GOOS == "windows" && runtime.GOARCH == "amd64":
		return "tunneltab.exe"
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return "tunneltab-linux-amd64"
	}
	return ""
}

// Size limits for downloads.
const (
	maxSums = 64 << 10
	maxSig  = 1 << 10
	maxZip  = 200 << 20
	maxFile = 150 << 20 // one file inside the zip
)

// Steps of "Update now", in order, as reported to Download's progress.
const (
	StepVerifying   = "verifying"   // downloading and checking the signed checksums
	StepDownloading = "downloading" // downloading the zip (Done/Total bytes)
	StepUnpacking   = "unpacking"   // checksum matched; unpacking the zip
	StepInstalling  = "installing"  // replacing the program files (Install)
)

// Progress is how far an update has got. Total is 0 when the size isn't known.
type Progress struct {
	Step  string `json:"step"`
	Done  int64  `json:"done,omitempty"`
	Total int64  `json:"total,omitempty"`
}

// Staged is a verified update, unpacked and ready for Install.
type Staged struct {
	Version string
	Dir     string            // where the new files are
	Files   map[string]string // target name in the app folder → staged file
}

// Download fetches the release r (from Check), checks it, and unpacks it
// into dir (which is emptied first). It installs nothing.
//
// Checks, in order: the checksum file is signed with pub; it lists the zip
// for exactly version r.Latest; the zip's SHA-256 matches; the zip has the
// program for this system. exeName is the running program's file name,
// which receives the new program for this system even if it was renamed.
// progress (may be nil) is called as each step starts and while the zip
// downloads.
func Download(ctx context.Context, client *http.Client, r Result, pub ed25519.PublicKey, dir, exeName string, progress func(Progress)) (Staged, error) {
	if !r.CanInstall {
		return Staged{}, errors.New("this release can't be installed automatically")
	}
	if progress == nil {
		progress = func(Progress) {}
	}
	progress(Progress{Step: StepVerifying})
	sums, err := fetch(ctx, client, r.assets[SumsName], maxSums)
	if err != nil {
		return Staged{}, err
	}
	sig, err := fetch(ctx, client, r.assets[SigName], maxSig)
	if err != nil {
		return Staged{}, err
	}
	if err := VerifySums(pub, sums, sig); err != nil {
		return Staged{}, err
	}
	list, err := ParseSums(sums)
	if err != nil {
		return Staged{}, ErrBadSignature // signed, yet unreadable: don't trust it
	}
	zipName := ZipName(r.Latest)
	want, ok := list[zipName]
	if !ok {
		return Staged{}, fmt.Errorf("the signed checksums don't list %s", zipName)
	}

	if err := os.RemoveAll(dir); err != nil {
		return Staged{}, fmt.Errorf("can't prepare the update folder: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Staged{}, fmt.Errorf("TunnelTab's folder can't be written to: %w", err)
	}
	zipPath := filepath.Join(dir, zipName)
	got, err := fetchToFile(ctx, client, r.assets[zipName], zipPath, maxZip, r.sizes[zipName], progress)
	if err != nil {
		return Staged{}, err
	}
	if got != want {
		return Staged{}, errors.New("the download doesn't match its signed checksum")
	}
	progress(Progress{Step: StepUnpacking})

	files, err := unpack(zipPath, filepath.Join(dir, "new"), exeName)
	if err != nil {
		return Staged{}, err
	}
	return Staged{Version: r.Latest, Dir: dir, Files: files}, nil
}

// unpack extracts the package files from the zip's top-level "tunneltab/"
// folder. Everything else in the zip is ignored, so names can't escape out.
func unpack(zipPath, outDir, exeName string) (map[string]string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, errors.New("the download isn't a valid zip file")
	}
	defer zr.Close()
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, n := range PackageFiles {
		known[n] = true
	}
	thisExe := ExeName()
	files := map[string]string{}
	for _, f := range zr.File {
		dir, name := path.Split(f.Name)
		if dir != "tunneltab/" || !known[name] || f.FileInfo().IsDir() {
			continue
		}
		if f.UncompressedSize64 > maxFile {
			return nil, fmt.Errorf("%s in the download is too large", name)
		}
		target := name
		if name == thisExe {
			target = exeName
		} else if name == exeName {
			continue // the running program gets thisExe instead
		}
		if _, dup := files[target]; dup {
			return nil, fmt.Errorf("%s is in the download twice", name)
		}
		out := filepath.Join(outDir, name)
		if err := extract(f, out); err != nil {
			return nil, err
		}
		files[target] = out
	}
	if _, ok := files[exeName]; !ok || thisExe == "" {
		return nil, errors.New("the download has no TunnelTab program for this system")
	}
	return files, nil
}

func extract(f *zip.File, out string) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("can't read %s from the download: %w", f.Name, err)
	}
	defer rc.Close()
	w, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	n, err := io.Copy(w, io.LimitReader(rc, maxFile+1))
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxFile {
		err = fmt.Errorf("%s in the download is too large", f.Name)
	}
	if err != nil {
		return fmt.Errorf("can't unpack %s: %w", f.Name, err)
	}
	return nil
}

func get(ctx context.Context, client *http.Client, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "TunnelTab-update")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("can't download the update: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("can't download the update: GitHub answered %s", resp.Status)
	}
	return resp, nil
}

// fetch downloads a small file, failing if it's larger than limit.
func fetch(ctx context.Context, client *http.Client, u string, limit int64) ([]byte, error) {
	resp, err := get(ctx, client, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("can't download the update: %w", err)
	}
	if int64(len(b)) > limit {
		return nil, errors.New("a release file is unexpectedly large")
	}
	return b, nil
}

// fetchToFile downloads into a new file and returns its hex SHA-256. It
// reports StepDownloading progress; the total is the response's length, or
// apiSize (from the release API) if the server doesn't say.
func fetchToFile(ctx context.Context, client *http.Client, u, out string, limit, apiSize int64, progress func(Progress)) (string, error) {
	resp, err := get(ctx, client, u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	w, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	total := resp.ContentLength
	if total <= 0 {
		total = apiSize
	}
	if total < 0 || total > limit {
		total = 0 // unknown, or too large anyway (the limit below says so)
	}
	h := sha256.New()
	c := &counter{progress: progress, total: total}
	c.report()
	n, err := io.Copy(io.MultiWriter(w, h, c), io.LimitReader(resp.Body, limit+1))
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("can't download the update: %w", err)
	}
	if n > limit {
		return "", errors.New("the download is unexpectedly large")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// counter reports download progress about every 1% (or every 256 KB when
// the size isn't known), so a download sends at most about 100 reports.
type counter struct {
	progress     func(Progress)
	done, total  int64
	lastReported int64
}

func (c *counter) Write(p []byte) (int, error) {
	c.done += int64(len(p))
	step := int64(256 << 10)
	if c.total > 0 {
		step = max(c.total/100, 1)
	}
	if c.done-c.lastReported >= step || (c.total > 0 && c.done == c.total) {
		c.report()
	}
	return len(p), nil
}

func (c *counter) report() {
	c.lastReported = c.done
	c.progress(Progress{Step: StepDownloading, Done: c.done, Total: c.total})
}
