package core

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

const yspDeviceAppID = "9f5c54c4ed0e50109b800f7e28fec205"
const yspDeviceVersion = "1.4.1"
const yspDeviceAppName = "央视频电视投屏助手"
const yspDevicePublicKey = "MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAkKeLy4ywWLSnBkwRyqYgF3HMIj05V5uuh5HjyEsZOWnu1NHu3jPQv3sr32wwQNYv5qapsNXmNgLUDHtgHZxqPQAYXltjSRc0qhcD286t62wOIHId8zXS3s1Jy4rgU4qjQWzI9rp/1sE0pMsmwTaJa4zuJ5iz8VwF8Av5oJ1k+HxY+/HLnjNlW1hmWLpuDYmkZYuAoTHa1VGeHQh9FEKI8ZcL3GTQphShUoC+Kg3P1hGUVTtCYapmzPS5lkAdwebuzwvTCfGiTErYZCnPBUSeV7BVlgjtLYIi29KvF0a8FHsJMfe/UdHcyW/RihsIYOtDQcRRpFGXyPXbVrzFJse24QIDAQAB"

type yspDeviceProfile struct {
	AndroidID    string `json:"android_id"`
	MAC          string `json:"mac"`
	Hardware     string `json:"hardware"`
	Board        string `json:"board"`
	Brand        string `json:"brand"`
	Device       string `json:"device"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Product      string `json:"product"`
	Tags         string `json:"tags"`
	BuildType    string `json:"build_type"`
	User         string `json:"user"`
	Resolution   string `json:"resolution"`
	Display      string `json:"display"`
	VersionID    string `json:"version_id"`
	Host         string `json:"host"`
	Fingerprint  string `json:"fingerprint"`
	ReportModel  string `json:"report_model"`
}

type yspDeviceState struct {
	SchemaVersion int              `json:"schema_version"`
	ProfileSource string           `json:"profile_source"`
	Profile       yspDeviceProfile `json:"profile"`
	ScreenParam   string           `json:"screen_param"`
	CastModel     string           `json:"cast_model"`
	UID           string           `json:"x_uid"`
	CloudGUID     string           `json:"cloud_guid"`
	RegisteredAt  float64          `json:"registered_at"`
	UpdatedAt     float64          `json:"updated_at"`
}

func yspNewDeviceState() (yspDeviceState, error) {
	noise, err := yspRandom(14)
	if err != nil {
		return yspDeviceState{}, err
	}
	mac := noise[8:]
	mac[0] = (mac[0] | 2) & 254
	profile := yspDeviceProfile{
		AndroidID: hex.EncodeToString(noise[:8]), MAC: fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", mac[0], mac[1], mac[2], mac[3], mac[4], mac[5]),
		Hardware: "mt5895", Board: "mt5895", Brand: "Sony", Manufacturer: "Sony", Model: "XR-85Z9K", ReportModel: "XR85Z9K",
		Device: "sony_xr_85z9k", Product: "sony_xr_85z9k", Tags: "release-keys", BuildType: "user", User: "build", Resolution: "7680*4320",
		VersionID: "SONYTV.2022.XR_85Z9K", Display: "XR-85Z9K-user 13 SONYTV.2022.XR_85Z9K 2024 release-keys", Host: "sony-tv-build",
		Fingerprint: "Sony/sony_xr_85z9k/sony_xr_85z9k:13/SONYTV.2022.XR_85Z9K/2024:user/release-keys",
	}
	return yspDeviceState{SchemaVersion: 1, ProfileSource: "sony_8k_pool.XR-85Z9K", Profile: profile, ScreenParam: "7680-4320-280", CastModel: profile.Model, UID: yspDeviceUID(profile)}, nil
}

func yspLoadDeviceState(path string) (yspDeviceState, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		state, err := yspNewDeviceState()
		if err == nil {
			err = yspSaveDeviceState(path, state)
		}
		return state, err
	}
	if err != nil {
		return yspDeviceState{}, errors.New("无法读取央视频设备身份")
	}
	var state yspDeviceState
	if len(b) > 64<<10 || json.Unmarshal(b, &state) != nil || state.SchemaVersion != 1 || len(state.Profile.AndroidID) != 16 || state.Profile.MAC == "" || state.Profile.Model == "" {
		return state, errors.New("央视频设备身份文件无效，已保留原文件")
	}
	if _, err := hex.DecodeString(state.Profile.AndroidID); err != nil {
		return state, errors.New("央视频设备标识无效，已保留原文件")
	}
	state.UID = yspDeviceUID(state.Profile)
	if state.ScreenParam == "" {
		state.ScreenParam = "7680-4320-280"
	}
	if state.CastModel == "" {
		state.CastModel = state.Profile.Model
	}
	return state, nil
}

func yspSaveDeviceState(path string, state yspDeviceState) error {
	state.UpdatedAt = float64(time.Now().UnixMilli()) / 1000
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeNativeCacheFile(path, append(b, '\n'))
}

func yspJavaHash(value string) int64 {
	var hash uint32
	for _, ch := range value {
		hash = hash*31 + uint32(ch)
	}
	return int64(int32(hash))
}

func yspDeviceUID(p yspDeviceProfile) string {
	build := "1698" + p.Hardware + p.Board + p.Brand + p.Device + p.Manufacturer + p.Model + p.Product + p.Tags + p.BuildType + p.User + p.Resolution + p.MAC
	id := fmt.Sprintf("%016x%016x", uint64(yspJavaHash(build)), uint64(yspJavaHash(p.Model)))
	digest := sha1.Sum([]byte(p.AndroidID + "|" + id))
	return strings.ToUpper(hex.EncodeToString(digest[:]))
}

func yspDeviceFingerprint(uid string, timestamp int64) string {
	day := ((timestamp/1000+28800)/86400)*86400000 - 28800000
	first := sha256.Sum256([]byte(fmt.Sprintf("%s%s%d%d", yspDeviceAppID, uid, timestamp, day)))
	second := sha256.Sum256([]byte(hex.EncodeToString(first[:])))
	return hex.EncodeToString(second[:])
}

func yspDeviceGCM(key string) (cipher.AEAD, error) {
	var raw [32]byte
	copy(raw[:], key)
	block, err := aes.NewCipher(raw[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func yspDeviceDecrypt(value, key string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) < 28 {
		return "", errors.New("央视频设备协议加密数据无效")
	}
	gcm, err := yspDeviceGCM(key)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, raw[:12], raw[12:], nil)
	if err != nil || !utf8.Valid(plain) {
		return "", &yspDeviceError{stage: "解密", invalidate: true}
	}
	return string(plain), nil
}

func yspDeviceEncrypt(value, key string) (string, error) {
	gcm, err := yspDeviceGCM(key)
	if err != nil {
		return "", err
	}
	nonce, err := yspRandom(gcm.NonceSize())
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(value), nil)), nil
}

func yspDeviceRSA(value string) (string, error) {
	der, err := base64.StdEncoding.DecodeString(yspDevicePublicKey)
	if err != nil {
		return "", err
	}
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return "", err
	}
	public, ok := key.(*rsa.PublicKey)
	if !ok {
		return "", errors.New("央视频设备协议公钥无效")
	}
	encrypted, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, public, []byte(value), nil)
	return base64.StdEncoding.EncodeToString(encrypted), err
}

func yspDeviceNonce() (string, error) {
	raw, err := yspRandom(16)
	if err != nil {
		return "", err
	}
	raw[6] = raw[6]&15 | 64
	raw[8] = raw[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:]), nil
}

func yspDeviceRandomString() (string, error) {
	raw, err := yspRandom(8)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%08x-0000-%04x-0000-00000000%04x", binary.BigEndian.Uint32(raw), binary.BigEndian.Uint16(raw[4:]), binary.BigEndian.Uint16(raw[6:])), nil
}

func yspDeviceJSON(value any) ([]byte, error) {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'}), nil
}
