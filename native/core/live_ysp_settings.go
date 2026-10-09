package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type nativeLiveSettings struct {
	DeviceMode     string `json:"deviceMode"`
	LinksPerDevice int    `json:"linksPerDevice"`
	CacheMB        int    `json:"cacheMB"`
	GatewayLAN     bool   `json:"gatewayLAN"`
	GatewayPort    int    `json:"gatewayPort"`
	Warning        string `json:"warning,omitempty"`
}

func defaultLiveSettings() nativeLiveSettings {
	return nativeLiveSettings{DeviceMode: "all", LinksPerDevice: 6, CacheMB: 200, GatewayPort: 8767}
}

func (settings nativeLiveSettings) validate() error {
	if settings.DeviceMode != "all" && settings.DeviceMode != "4k" && settings.DeviceMode != "off" {
		return errors.New("直播设备模式无效")
	}
	if settings.LinksPerDevice < 0 || settings.LinksPerDevice > 26 || settings.CacheMB < 0 || settings.CacheMB > 512 {
		return errors.New("设备配额应为 0 至 26，直播缓存应为 0 至 512 MB")
	}
	if settings.GatewayPort < 1024 || settings.GatewayPort > 65535 {
		return errors.New("订阅服务端口应为 1024 至 65535")
	}
	return nil
}

func yspLoadLiveSettings(directory string) nativeLiveSettings {
	settings := defaultLiveSettings()
	path := filepath.Join(directory, "live-settings.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return settings
	}
	if err != nil || len(data) > 16384 || json.Unmarshal(data, &settings) != nil || settings.validate() != nil {
		settings = defaultLiveSettings()
		settings.Warning = "直播设置无法读取，已使用默认值；原文件已保留"
	}
	return settings
}

func (engine *nativeEngine) saveLiveSettings(settings nativeLiveSettings) (nativeLiveSettings, error) {
	settings.Warning = ""
	if err := settings.validate(); err != nil {
		return settings, err
	}
	live, err := engine.liveServer()
	if err != nil {
		return settings, err
	}
	live.mu.Lock()
	busy := live.gatewayServer != nil && (live.settings.GatewayLAN != settings.GatewayLAN || live.settings.GatewayPort != settings.GatewayPort)
	live.mu.Unlock()
	if busy {
		return settings, errors.New("请先关闭订阅服务，再更改访问范围或端口")
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return settings, err
	}
	if err := writeNativeCacheFile(filepath.Join(engine.directory, "live-settings.json"), data); err != nil {
		return settings, errors.New("直播设置保存失败")
	}
	live.mu.Lock()
	previous := live.settings
	live.settings = settings
	active := live.server != nil
	if previous.DeviceMode != settings.DeviceMode || previous.LinksPerDevice != settings.LinksPerDevice {
		live.cache = map[string]yspLiveState{}
	}
	live.mu.Unlock()
	live.segmentCache.resize(settings.CacheMB)
	if pool, ok := live.device.(*yspDevicePool); ok {
		pool.configure(settings.DeviceMode, settings.LinksPerDevice)
		if previous.DeviceMode != settings.DeviceMode {
			pool.stop()
			if active {
				pool.start()
			}
		}
	}
	live.event("settings", "直播运行设置已更新", "")
	return settings, nil
}

func (live *yspLiveServer) deviceEnabled(channel yspChannel) bool {
	live.mu.Lock()
	mode := live.settings.DeviceMode
	live.mu.Unlock()
	return mode != "off" && (mode != "4k" || yspWarmChannel(channel.ID))
}
