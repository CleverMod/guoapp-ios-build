package core

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const yspDeviceLinkLimit = 6

type yspDeviceSource interface {
	start()
	stop()
	reopen(string)
	resolve(context.Context, yspChannel) (yspDeviceEntry, error)
	accept(yspChannel, yspDeviceEntry)
	reject(yspChannel, yspDeviceEntry)
}

type yspDevicePool struct {
	mu           sync.Mutex
	control      chan struct{}
	slots        [3]*yspDeviceResolver
	standby      *yspDeviceResolver
	assignments  map[string]int
	lastRotation [3]time.Time
	cancel       context.CancelFunc
	life         context.Context
	refilling    bool
	lastRefill   time.Time
	mode         string
	linkLimit    int
	logEvent     func(string, string, string)
}

func newYSPDevicePool(client *http.Client, directory string) *yspDevicePool {
	pool := &yspDevicePool{control: make(chan struct{}, 1), assignments: map[string]int{}, mode: "all", linkLimit: yspDeviceLinkLimit}
	for index := range pool.slots {
		device := newYSPDeviceResolver(client, directory)
		device.fast, device.warm = true, index == 0
		if index > 0 {
			device.path = filepath.Join(directory, "ysp-device-state-slot"+strconv.Itoa(index)+".json")
		}
		pool.slots[index] = device
	}
	pool.standby = newYSPDeviceResolver(client, directory)
	pool.standby.path, pool.standby.fast, pool.standby.warm = "", true, false
	return pool
}

func (pool *yspDevicePool) start() {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.cancel != nil || pool.mode == "off" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool.cancel = cancel
	pool.life = ctx
	for _, device := range append(pool.slots[:], pool.standby) {
		device.mu.Lock()
		device.life = ctx
		device.mu.Unlock()
	}
	go pool.run(ctx)
}

func (pool *yspDevicePool) stop() {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.cancel != nil {
		pool.cancel()
		pool.cancel = nil
	}
}

func (pool *yspDevicePool) run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		for index, device := range pool.slots {
			pool.mu.Lock()
			mode := pool.mode
			pool.mu.Unlock()
			if mode == "off" || mode == "4k" && index > 0 {
				continue
			}
			work, cancel := context.WithTimeout(ctx, 45*time.Second)
			device.mu.Lock()
			session := device.session
			renew := index == 0 && session != nil && time.Since(session.created) >= yspDeviceSessionTTL-5*time.Minute
			device.mu.Unlock()
			if renew {
				pool.rotate(work, index)
			}
			device.pulse(work)
			cancel()
			if index == 0 {
				pool.replenish()
			}
			if ctx.Err() != nil {
				return
			}
		}
		pool.standby.mu.Lock()
		ready := pool.standby.session != nil
		pool.standby.mu.Unlock()
		if ready {
			work, cancel := context.WithTimeout(ctx, 45*time.Second)
			pool.standby.pulse(work)
			cancel()
		} else {
			pool.replenish()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (pool *yspDevicePool) replenish() {
	pool.mu.Lock()
	if pool.refilling || pool.life == nil || pool.life.Err() != nil {
		pool.mu.Unlock()
		return
	}
	pool.refilling = true
	life, delay := pool.life, time.Until(pool.lastRefill.Add(15*time.Second))
	pool.mu.Unlock()
	go func() {
		defer func() {
			pool.mu.Lock()
			pool.refilling = false
			pool.mu.Unlock()
		}()
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-life.Done():
				return
			case <-timer.C:
			}
		}
		pool.mu.Lock()
		pool.lastRefill = time.Now()
		pool.mu.Unlock()
		ctx, cancel := context.WithTimeout(life, 45*time.Second)
		defer cancel()
		pool.standby.pulse(ctx)
	}()
}

func yspDeviceLinkedCount(device *yspDeviceResolver) int {
	device.mu.Lock()
	defer device.mu.Unlock()
	if device.session == nil {
		return 0
	}
	return len(device.session.linked)
}

func (pool *yspDevicePool) slotFor(channel string) int {
	if yspWarmChannel(channel) {
		return 0
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if slot, exists := pool.assignments[channel]; exists {
		return slot
	}
	first, second := yspDeviceLinkedCount(pool.slots[1]), yspDeviceLinkedCount(pool.slots[2])
	slot := 1
	if second < first {
		slot = 2
	}
	pool.assignments[channel] = slot
	return slot
}

func (pool *yspDevicePool) takeStandby() *yspDeviceSession {
	standby := pool.standby
	select {
	case standby.control <- struct{}{}:
		defer func() { <-standby.control }()
	default:
		return nil
	}
	standby.mu.Lock()
	defer standby.mu.Unlock()
	session := standby.session
	if !yspSessionFresh(session, time.Now()) || time.Since(session.created) >= yspDeviceSessionTTL-5*time.Minute {
		return nil
	}
	standby.session, standby.loaded = nil, false
	standby.sessionRetry = time.Time{}
	return session
}

func (pool *yspDevicePool) rotate(ctx context.Context, slot int) error {
	device := pool.slots[slot]
	if err := device.lockControl(ctx); err != nil {
		return err
	}
	defer func() { <-device.control }()
	pool.mu.Lock()
	tooSoon := time.Since(pool.lastRotation[slot]) < 8*time.Second
	if !tooSoon {
		pool.lastRotation[slot] = time.Now()
	}
	pool.mu.Unlock()
	if tooSoon {
		return errors.New("央视频设备正在轮换，请稍后重试")
	}
	session := pool.takeStandby()
	if session == nil {
		state, err := yspNewDeviceState()
		if err != nil {
			return err
		}
		device.device, device.loaded = state, true
		session, err = device.bootstrapFast(ctx)
		if err != nil {
			return err
		}
	}
	if err := yspSaveDeviceState(device.path, session.device); err != nil {
		return errors.New("无法保存央视频轮换设备")
	}
	device.mu.Lock()
	device.session, device.device, device.loaded = session, session.device, true
	device.sessionRetry, device.bootFailures = time.Time{}, 0
	device.failures = map[string]yspDeviceFailure{}
	device.mu.Unlock()
	pool.mu.Lock()
	pool.lastRotation[slot] = time.Now()
	for channel, assigned := range pool.assignments {
		if assigned == slot {
			delete(pool.assignments, channel)
		}
	}
	pool.mu.Unlock()
	pool.replenish()
	if pool.logEvent != nil {
		pool.logEvent("rotation", "设备槽位 "+strconv.Itoa(slot)+" 已轮换", "")
	}
	return nil
}

func (pool *yspDevicePool) resolve(ctx context.Context, channel yspChannel) (yspDeviceEntry, error) {
	select {
	case pool.control <- struct{}{}:
		defer func() { <-pool.control }()
	case <-ctx.Done():
		return yspDeviceEntry{}, ctx.Err()
	}
	slot := pool.slotFor(channel.ID)
	device := pool.slots[slot]
	pool.mu.Lock()
	limit := pool.linkLimit
	mode := pool.mode
	pool.mu.Unlock()
	if mode == "off" || mode == "4k" && !yspWarmChannel(channel.ID) {
		return yspDeviceEntry{}, errors.New("此频道已设置为标准直播模式")
	}
	device.mu.Lock()
	if entry, exists := device.entries[channel.ID]; exists && time.Now().Before(entry.expires) && entry.headers["UID"] != "" && entry.headers["APPSIGN"] != "" {
		entry.lastUsed = time.Now()
		device.entries[channel.ID] = entry
		device.lastViewed[channel.ID] = time.Now()
		device.mu.Unlock()
		return entry, nil
	}
	waiting := time.Now().Before(device.failures[channel.ID].retryAt)
	device.mu.Unlock()
	if waiting {
		return yspDeviceEntry{}, errors.New("央视频设备频道正在等待重试")
	}
	if err := pool.ensureSession(ctx, slot); err != nil {
		return yspDeviceEntry{}, err
	}
	device.mu.Lock()
	session := device.session
	full := limit > 0 && slot > 0 && session != nil && !session.linked[channel.ID] && len(session.linked) >= limit
	device.mu.Unlock()
	if full {
		other := 3 - slot
		if err := pool.ensureSession(ctx, other); err == nil && yspDeviceLinkedCount(pool.slots[other]) < limit {
			slot, device = other, pool.slots[other]
		} else if err := pool.rotate(ctx, slot); err != nil {
			if !errors.Is(err, context.Canceled) {
				device.mu.Lock()
				device.recordFailureLocked(channel.ID)
				device.mu.Unlock()
			}
			return yspDeviceEntry{}, err
		}
		pool.mu.Lock()
		pool.assignments[channel.ID] = slot
		pool.mu.Unlock()
	}
	entry, err := device.resolve(ctx, channel)
	if err != nil && yspDeviceInvalidates(err) && ctx.Err() == nil {
		if rotateErr := pool.rotate(ctx, slot); rotateErr == nil {
			device.reopen(channel.ID)
			entry, err = device.resolve(ctx, channel)
		}
	}
	return entry, err
}

func (pool *yspDevicePool) configure(mode string, limit int) {
	pool.mu.Lock()
	pool.mode, pool.linkLimit = mode, limit
	pool.mu.Unlock()
}

func (pool *yspDevicePool) ensureSession(ctx context.Context, slot int) error {
	device := pool.slots[slot]
	device.mu.Lock()
	ready := yspSessionFresh(device.session, time.Now()) && time.Until(device.session.created.Add(yspDeviceSessionTTL)) > 5*time.Minute
	retry := device.sessionRetry
	device.mu.Unlock()
	if ready {
		return nil
	}
	if slot == 0 {
		pool.standby.mu.Lock()
		standbyReady := yspSessionFresh(pool.standby.session, time.Now())
		pool.standby.mu.Unlock()
		if standbyReady {
			return pool.rotate(ctx, slot)
		}
	}
	if time.Now().Before(retry) {
		return errors.New("央视频设备注册正在等待重试")
	}
	if err := device.lockControl(ctx); err != nil {
		return err
	}
	defer func() { <-device.control }()
	device.mu.Lock()
	ready = yspSessionFresh(device.session, time.Now()) && time.Until(device.session.created.Add(yspDeviceSessionTTL)) > 5*time.Minute
	device.mu.Unlock()
	if ready {
		return nil
	}
	session, err := device.bootstrapFast(ctx)
	if err != nil {
		if ctx.Err() == nil {
			device.mu.Lock()
			device.bootFailures++
			device.sessionRetry = time.Now().Add(15 * time.Second)
			device.mu.Unlock()
		}
		return err
	}
	device.mu.Lock()
	device.session, device.sessionRetry, device.bootFailures = session, time.Time{}, 0
	device.mu.Unlock()
	pool.replenish()
	return nil
}

func (pool *yspDevicePool) retryPlaylist(ctx context.Context, channel yspChannel, rejected yspDeviceEntry, rotate bool) (yspDeviceEntry, error) {
	device := rejected.owner
	if device == nil {
		return yspDeviceEntry{}, errors.New("央视频设备线路身份无效")
	}
	device.mu.Lock()
	if entry := device.entries[channel.ID]; entry.address == rejected.address {
		delete(device.entries, channel.ID)
	}
	delete(device.failures, channel.ID)
	device.mu.Unlock()
	if rotate {
		for slot, candidate := range pool.slots {
			if candidate == device {
				if err := pool.rotate(ctx, slot); err != nil {
					return yspDeviceEntry{}, err
				}
				break
			}
		}
	}
	return pool.resolve(ctx, channel)
}

func (pool *yspDevicePool) reopen(channel string) {
	for _, device := range pool.slots {
		device.reopen(channel)
	}
}

func (pool *yspDevicePool) accept(channel yspChannel, entry yspDeviceEntry) {
	if entry.owner != nil {
		entry.owner.accept(channel, entry)
	}
}

func (pool *yspDevicePool) reject(channel yspChannel, entry yspDeviceEntry) {
	if entry.owner != nil {
		entry.owner.mu.Lock()
		_, exists := entry.owner.entries[channel.ID]
		if !exists && !time.Now().Before(entry.owner.failures[channel.ID].retryAt) {
			entry.owner.recordFailureLocked(channel.ID)
		}
		entry.owner.mu.Unlock()
		entry.owner.reject(channel, entry)
	}
}
